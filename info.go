package main

import (
	"fmt"
	"image"
	"reflect"
	"sort"
	"strings"
	"sync"

	"github.com/aarzilli/nucular"
	"github.com/aarzilli/nucular/label"
	ntypes "github.com/aarzilli/nucular/types"
	"github.com/derekparker/delve/service/api"
)

func init() {
	// used to break initialization loop
	goroutinesPanel.update = updateGoroutines
	stackPanel.update = updateStacktrace
	threadsPanel.update = updateThreads
	breakpointsPanel.update = updateBreakpoints
}

type infoPanel struct {
	mu      sync.Mutex
	loaded  bool
	loading bool
	name    string
	update  func(p *infoPanel, mw *nucular.MasterWindow, w *nucular.Window)
	load    func(p *infoPanel)
}

func (l *infoPanel) clear() {
	l.mu.Lock()
	l.loaded = false
	l.mu.Unlock()
}

func (l *infoPanel) done() {
	l.mu.Lock()
	l.loading = false
	l.loaded = true
	l.mu.Unlock()
	wnd.Changed()
}

var goroutinesPanel = &infoPanel{
	name: "goroutines",
	load: loadGoroutines,
}

var stackPanel = &infoPanel{
	name: "stacktrace",
	load: loadStacktrace,
}

var threadsPanel = &infoPanel{
	name: "threads",
	load: loadThreads,
}

var localsPanel = &infoPanel{
	name:   "locals",
	update: updateLocals,
	load:   loadLocals,
}

var regsPanel = &infoPanel{
	name:   "regs",
	update: updateRegs,
	load:   loadRegs,
}

var globalsPanel = &infoPanel{
	name:   "globals",
	update: updateGlobals,
	load:   loadGlobals,
}

var breakpointsPanel = &infoPanel{
	name: "breakpoints",
	load: loadBreakpoints,
}

const (
	currentGoroutineLocation = "Current location"
	userGoroutineLocation    = "User location"
	goStatementLocation      = "Go statement location"
)

var goroutineLocations = []string{currentGoroutineLocation, userGoroutineLocation, goStatementLocation}
var goroutineLocation int = 1

var goroutines []*api.Goroutine
var stack []api.Stackframe
var stackDepth int = 20
var threads []*api.Thread

var localsFilterEditor = nucular.TextEditor{Filter: spacefilter}
var localsShowAddress bool = false
var args []api.Variable
var locals []api.Variable
var regs string
var globalsFilterEditor = nucular.TextEditor{Filter: spacefilter}
var globalsShowAddress bool = false
var globals []api.Variable
var selectedBreakpoint int
var breakpoints []*api.Breakpoint
var funcsFilterEditor = nucular.TextEditor{Filter: spacefilter}
var functions []string
var typesFilterEditor = nucular.TextEditor{Filter: spacefilter}
var types []string
var sourcesFilterEditor = nucular.TextEditor{Filter: spacefilter}
var sources []string

func spacefilter(ch rune) bool {
	return ch != ' ' && ch != '\t'
}

func (p *infoPanel) Update(mw *nucular.MasterWindow, container *nucular.Window) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.loading {
		container.Label("Loading...", "LT")
		return
	}

	if !p.loaded {
		if client == nil {
			container.Label("Connecting...", "LT")
			return
		}
		if running {
			container.Label("Running...", "LT")
			return
		}

		p.loading = true
		go p.load(p)
	}

	if w := container.GroupBegin(p.name, 0); w != nil {
		p.update(p, mw, w)
		w.GroupEnd()
	}
}

type goroutinesByID []*api.Goroutine

func (gs goroutinesByID) Len() int { return len(gs) }
func (gs goroutinesByID) Swap(i, j int) {
	temp := gs[i]
	gs[i] = gs[j]
	gs[j] = temp
}
func (gs goroutinesByID) Less(i, j int) bool { return gs[i].ID < gs[j].ID }

func loadGoroutines(p *infoPanel) {
	out := editorWriter{&scrollbackEditor, true}
	var err error
	goroutines, err = client.ListGoroutines()
	if err != nil {
		fmt.Fprintf(&out, "Could not list goroutines: %v\n", err)
		return
	}
	sort.Sort(goroutinesByID(goroutines))
	p.done()
}

func updateGoroutines(p *infoPanel, mw *nucular.MasterWindow, w *nucular.Window) {
	style, _ := mw.Style()

	w.MenubarBegin()
	w.Row(20).Static(180)
	w.ComboSimple(goroutineLocations, &goroutineLocation, 22)
	w.MenubarEnd()

	pad := style.Selectable.Padding.X * 2
	d := 1
	if len(goroutines) > 0 {
		d = digits(goroutines[len(goroutines)-1].ID)
	}

	maxthreadid := 0
	for _, g := range goroutines {
		if g.ThreadID > maxthreadid {
			maxthreadid = g.ThreadID
		}
	}

	dthread := digits(maxthreadid)

	zerow := nucular.FontWidth(style.Font, "0")

	w.Row(20).StaticScaled(zerow*d+pad, zerow*dthread+pad, 0)
	for _, g := range goroutines {
		selected := curGid == g.ID
		w.SelectableLabel(fmt.Sprintf("%*d", d, g.ID), "LC", &selected)
		if g.ThreadID != 0 {
			w.SelectableLabel(fmt.Sprintf("%*d", dthread, g.ThreadID), "LC", &selected)
		} else {
			w.SelectableLabel(" ", "LC", &selected)
		}
		switch goroutineLocations[goroutineLocation] {
		case currentGoroutineLocation:
			w.SelectableLabel(formatLocation(g.CurrentLoc), "LC", &selected)
		case userGoroutineLocation:
			w.SelectableLabel(formatLocation(g.UserCurrentLoc), "LC", &selected)
		case goStatementLocation:
			w.SelectableLabel(formatLocation(g.GoStatementLoc), "LC", &selected)
		}
		if selected && curGid != g.ID && !running {
			go func(gid int) {
				state, err := client.SwitchGoroutine(gid)
				if err != nil {
					out := editorWriter{&scrollbackEditor, true}
					fmt.Fprintf(&out, "Could not switch goroutine: %v\n", err)
				} else {
					go refreshState(false, clearGoroutineSwitch, state)
				}
			}(g.ID)
		}
	}
}

func loadStacktrace(p *infoPanel) {
	out := editorWriter{&scrollbackEditor, true}
	var err error
	stack, err = client.Stacktrace(curGid, stackDepth, nil)
	if err != nil {
		fmt.Fprintf(&out, "Could not stacktrace: %v\n", err)
		return
	}
	p.done()
}

func updateStacktrace(p *infoPanel, mw *nucular.MasterWindow, w *nucular.Window) {
	style, _ := mw.Style()

	w.MenubarBegin()
	w.Row(20).Static(120)
	if w.PropertyInt("depth:", 1, &stackDepth, 200, 1, 5) {
		go func() {
			p.clear()
			wnd.Changed()
		}()
	}
	w.MenubarEnd()

	maxpc := uint64(0)
	for _, frame := range stack {
		if frame.PC > maxpc {
			maxpc = frame.PC
		}
	}

	pad := style.Selectable.Padding.X * 2
	didx := digits(len(stack))
	d := hexdigits(maxpc)

	w.Row(40).StaticScaled(nucular.FontWidth(style.Font, "0")*didx+pad, nucular.FontWidth(style.Font, fmt.Sprintf("%#0*x", d, 0))+pad, 0)

	for i, frame := range stack {
		selected := curFrame == i
		w.SelectableLabel(fmt.Sprintf("%*d", didx, i), "LT", &selected)
		w.SelectableLabel(fmt.Sprintf("%#0*x", d, frame.PC), "LT", &selected)
		name := "(nil)"
		if frame.Function != nil {
			name = frame.Function.Name
		}
		w.SelectableLabel(fmt.Sprintf("%s\nat %s:%d", name, ShortenFilePath(frame.File), frame.Line), "LT", &selected)
		if selected && curFrame != i && !running {
			curFrame = i
			go refreshState(true, clearFrameSwitch, nil)
		}
	}
}

type threadsByID []*api.Thread

func (threads threadsByID) Len() int { return len(threads) }
func (threads threadsByID) Swap(i, j int) {
	temp := threads[i]
	threads[i] = threads[j]
	threads[j] = temp
}
func (threads threadsByID) Less(i, j int) bool { return threads[i].ID < threads[j].ID }

func loadThreads(p *infoPanel) {
	out := editorWriter{&scrollbackEditor, true}
	var err error
	threads, err = client.ListThreads()
	if err != nil {
		fmt.Fprintf(&out, "Could not list threads: %v\n", err)
		return
	}
	sort.Sort(threadsByID(threads))
	p.done()
}

func updateThreads(p *infoPanel, mw *nucular.MasterWindow, w *nucular.Window) {
	style, _ := mw.Style()

	pad := style.Selectable.Padding.X * 2
	d := 1
	if len(threads) > 0 {
		d = digits(threads[len(threads)-1].ID)
	}
	w.Row(20).StaticScaled(nucular.FontWidth(style.Font, "0")*d+pad, 0)

	for _, thread := range threads {
		selected := curThread == thread.ID
		w.SelectableLabel(fmt.Sprintf("%*d", d, thread.ID), "LC", &selected)
		loc := api.Location{thread.PC, thread.File, thread.Line, thread.Function}
		w.SelectableLabel(formatLocation(loc), "LC", &selected)
		if selected && curThread != thread.ID && !running {
			go func(tid int) {
				state, err := client.SwitchThread(tid)
				if err != nil {
					out := editorWriter{&scrollbackEditor, true}
					fmt.Fprintf(&out, "Could not switch thread: %v\n", err)
				} else {
					go refreshState(false, clearGoroutineSwitch, state)
				}
			}(thread.ID)
		}
	}
	w.GroupEnd()
}

type variablesByName []api.Variable

func (vars variablesByName) Len() int { return len(vars) }
func (vars variablesByName) Swap(i, j int) {
	temp := vars[i]
	vars[i] = vars[j]
	vars[j] = temp
}
func (vars variablesByName) Less(i, j int) bool { return vars[i].Name < vars[j].Name }

func loadLocals(p *infoPanel) {
	m := map[string]int{}

	out := editorWriter{&scrollbackEditor, true}
	var err error
	args, err = client.ListFunctionArgs(api.EvalScope{curGid, curFrame}, LongLoadConfig)
	if err != nil {
		fmt.Fprintf(&out, "Could not list function arguments: %v\n", err)
		return
	}
	locals, err = client.ListLocalVariables(api.EvalScope{curGid, curFrame}, LongLoadConfig)
	if err != nil {
		fmt.Fprintf(&out, "Could not list local variables: %v\n", err)
		return
	}
	sort.Sort(variablesByName(args))
	sort.Sort(variablesByName(locals))

	changename := func(v *api.Variable) {
		if n, ok := m[v.Name]; ok {
			n++
			m[v.Name] = n
			v.Name = fmt.Sprintf("%s(%d)", v.Name, n)
		} else {
			m[v.Name] = 0
		}
	}

	for i := range args {
		changename(&args[i])
	}
	for i := range locals {
		changename(&locals[i])
	}
	p.done()
}

const (
	varRowHeight = 20
	moreBtnWidth = 70
)

func updateLocals(p *infoPanel, mw *nucular.MasterWindow, w *nucular.Window) {
	w.MenubarBegin()
	w.Row(varRowHeight).Static(90, 0, 100)
	w.Label("Filter:", "LC")
	localsFilterEditor.Edit(w)
	filter := string(localsFilterEditor.Buffer)
	w.CheckboxText("Address", &localsShowAddress)
	w.MenubarEnd()
	w.Row(varRowHeight).Dynamic(1)

	_, scaling := mw.Style()
	ind := int(18 * scaling)

	for i := range args {
		if strings.Index(args[i].Name, filter) >= 0 {
			showVariable(w, 0, localsShowAddress, args[i].Name, &args[i], ind)
		}
	}

	if len(args) > 0 {
		w.Row(varRowHeight / 2).Dynamic(1)
		w.Spacing(1)
		w.Row(varRowHeight).Dynamic(1)
	}

	for i := range locals {
		if strings.Index(locals[i].Name, filter) >= 0 {
			showVariable(w, 0, localsShowAddress, locals[i].Name, &locals[i], ind)
		}
	}
}

func loadRegs(p *infoPanel) {
	out := editorWriter{&scrollbackEditor, true}
	var err error
	regs, err = client.ListRegisters()
	if err != nil {
		fmt.Fprintf(&out, "Could not list registers: %v\n", err)
		return
	}
	p.done()
}

func updateRegs(p *infoPanel, mw *nucular.MasterWindow, w *nucular.Window) {
	lines := 1
	for i := range regs {
		if regs[i] == '\n' {
			lines++
		}
	}
	w.Row(20 * lines).Dynamic(1)
	w.Label(regs, "LT")
}

func loadGlobals(p *infoPanel) {
	out := editorWriter{&scrollbackEditor, true}
	var err error
	globals, err = client.ListPackageVariables("", LongLoadConfig)
	if err != nil {
		fmt.Fprintf(&out, "Could not list global variabless: %v\n", err)
		return
	}
	sort.Sort(variablesByName(globals))
	p.done()
}

func updateGlobals(p *infoPanel, mw *nucular.MasterWindow, w *nucular.Window) {
	w.MenubarBegin()
	w.Row(varRowHeight).Static(90, 0, 100)
	w.Label("Filter:", "LC")
	globalsFilterEditor.Edit(w)
	filter := string(globalsFilterEditor.Buffer)
	w.CheckboxText("Address", &globalsShowAddress)
	w.MenubarEnd()
	w.Row(varRowHeight).Dynamic(1)

	_, scaling := mw.Style()
	ind := int(18 * scaling)

	for i := range globals {
		if strings.Index(globals[i].Name, filter) >= 0 {
			showVariable(w, 0, globalsShowAddress, globals[i].Name, &globals[i], ind)
		}
	}
}

type breakpointsByID []*api.Breakpoint

func (bps breakpointsByID) Len() int { return len(bps) }
func (bps breakpointsByID) Swap(i, j int) {
	temp := bps[i]
	bps[i] = bps[j]
	bps[j] = temp
}
func (bps breakpointsByID) Less(i, j int) bool { return bps[i].ID < bps[i].ID }

func loadBreakpoints(p *infoPanel) {
	out := editorWriter{&scrollbackEditor, true}
	var err error
	breakpoints, err = client.ListBreakpoints()
	sort.Sort(breakpointsByID(breakpoints))
	if err != nil {
		fmt.Fprintf(&out, "Could not list breakpoints: %v\n", err)
		return
	}
	p.done()
}

func updateBreakpoints(p *infoPanel, mw *nucular.MasterWindow, w *nucular.Window) {
	style, _ := mw.Style()

	pad := style.Selectable.Padding.X * 2
	d := 1
	if len(breakpoints) > 0 {
		d = digits(breakpoints[len(breakpoints)-1].ID)
	}
	if d < 3 {
		d = 3
	}

	w.Row(40).StaticScaled(nucular.FontWidth(style.Font, "0")*d+pad, 0)
	for _, breakpoint := range breakpoints {
		selected := selectedBreakpoint == breakpoint.ID
		w.SelectableLabel(fmt.Sprintf("%*d", d, breakpoint.ID), "LT", &selected)
		bounds := w.LastWidgetBounds
		bounds.W = w.Bounds.W
		if w.Input().Mouse.AnyClickInRect(bounds) {
			selectedBreakpoint = breakpoint.ID
		}
		w.SelectableLabel(fmt.Sprintf("%s in %s\nat %s:%d (%#v)", breakpoint.Name, breakpoint.FunctionName, breakpoint.File, breakpoint.Line, breakpoint.Addr), "LT", &selected)
		if !running {
			w.ContextualOpen(0, image.Point{200, 500}, bounds, breakpointsMenu)
		}
	}
}

func breakpointsMenu(mw *nucular.MasterWindow, w *nucular.Window) {
	w.Row(20).Dynamic(1)
	if selectedBreakpoint > 0 {
		if w.MenuItem(label.TA("Edit...", "LC")) {
			mw.PopupOpen(fmt.Sprintf("Editing breakpoint %d", selectedBreakpoint), nucular.WindowDynamic|nucular.WindowTitle|nucular.WindowNoScrollbar|nucular.WindowMovable|nucular.WindowBorder, ntypes.Rect{100, 100, 400, 700}, true, breakpointEditor)
		}
		if w.MenuItem(label.TA("Clear", "LC")) {
			go func() {
				scrollbackOut := editorWriter{&scrollbackEditor, true}
				_, err := client.ClearBreakpoint(selectedBreakpoint)
				if err != nil {
					fmt.Fprintf(&scrollbackOut, "Could not clear breakpoint: %v\n", err)
				}
				breakpointsPanel.clear()
				wnd.Changed()
			}()
		}
	}
	if w.MenuItem(label.TA("Clear All", "LC")) {
		go func() {
			scrollbackOut := editorWriter{&scrollbackEditor, true}
			for i := range breakpoints {
				if breakpoints[i].ID < 0 {
					continue
				}
				_, err := client.ClearBreakpoint(breakpoints[i].ID)
				if err != nil {
					fmt.Fprintf(&scrollbackOut, "Could not clear breakpoint %d: %v\n", breakpoints[i].ID, err)
				}
			}
			breakpointsPanel.clear()
			wnd.Changed()
		}()
	}
}

var editedBreakpoint *api.Breakpoint
var breakpointPrintEditor nucular.TextEditor
var breakpointCondEditor nucular.TextEditor

func breakpointEditor(mw *nucular.MasterWindow, w *nucular.Window) {
	if editedBreakpoint == nil {
		for i := range breakpoints {
			if breakpoints[i].ID == selectedBreakpoint {
				editedBreakpoint = breakpoints[i]
				break
			}
		}

		if editedBreakpoint == nil {
			w.Close()
			return
		}

		breakpointPrintEditor.Flags = nucular.EditMultiline | nucular.EditClipboard | nucular.EditSelectable
		breakpointPrintEditor.Buffer = breakpointPrintEditor.Buffer[:0]
		for i := range editedBreakpoint.Variables {
			breakpointPrintEditor.Buffer = append(breakpointPrintEditor.Buffer, []rune(fmt.Sprintf("%s\n", editedBreakpoint.Variables[i]))...)
		}

		breakpointCondEditor.Flags = nucular.EditClipboard | nucular.EditSelectable
		breakpointCondEditor.Buffer = []rune(editedBreakpoint.Cond)
	}

	w.Row(20).Dynamic(2)
	if w.OptionText("breakpoint", !editedBreakpoint.Tracepoint) {
		editedBreakpoint.Tracepoint = false
	}
	if w.OptionText("tracepoint", editedBreakpoint.Tracepoint) {
		editedBreakpoint.Tracepoint = true
	}

	w.Row(20).Static(100, 100, 150)
	arguments := editedBreakpoint.LoadArgs != nil
	w.CheckboxText("Arguments", &arguments)
	locals := editedBreakpoint.LoadLocals != nil
	w.CheckboxText("Locals", &locals)
	w.PropertyInt("Stacktrace", 0, &editedBreakpoint.Stacktrace, 200, 1, 10)

	verboseArguments, verboseLocals := false, false
	w.Row(20).Static(20, 100, 100)
	w.Spacing(1)
	if arguments {
		verboseArguments = editedBreakpoint.LoadArgs != nil && *editedBreakpoint.LoadArgs == LongLoadConfig
		w.CheckboxText("-v", &verboseArguments)
	} else {
		w.Spacing(1)
	}
	if locals {
		verboseLocals = editedBreakpoint.LoadLocals != nil && *editedBreakpoint.LoadLocals == LongLoadConfig
		w.CheckboxText("-v", &verboseLocals)
	} else {
		w.Spacing(1)
	}

	if arguments {
		if verboseArguments {
			editedBreakpoint.LoadArgs = &LongLoadConfig
		} else {
			editedBreakpoint.LoadArgs = &ShortLoadConfig
		}
	} else {
		editedBreakpoint.LoadArgs = nil
	}

	if locals {
		if verboseLocals {
			editedBreakpoint.LoadLocals = &LongLoadConfig
		} else {
			editedBreakpoint.LoadLocals = &ShortLoadConfig
		}
	} else {
		editedBreakpoint.LoadLocals = nil
	}

	w.Row(20).Dynamic(1)
	w.Label("Print:", "LC")
	w.Row(100).Dynamic(1)
	breakpointPrintEditor.Edit(w)

	w.Row(20).Static(70, 0)
	w.Label("Condition:", "LC")
	breakpointCondEditor.Edit(w)

	w.Row(20).Static(0, 80, 80)
	w.Spacing(1)
	if w.ButtonText("Cancel") {
		editedBreakpoint = nil
		breakpointsPanel.clear()
		w.Close()
	}
	if w.ButtonText("OK") {
		editedBreakpoint.Cond = string(breakpointCondEditor.Buffer)
		editedBreakpoint.Variables = editedBreakpoint.Variables[:0]
		for _, p := range strings.Split(string(breakpointPrintEditor.Buffer), "\n") {
			if p == "" {
				continue
			}
			editedBreakpoint.Variables = append(editedBreakpoint.Variables, p)
		}
		go func(bp *api.Breakpoint) {
			err := client.AmendBreakpoint(bp)
			if err != nil {
				scrollbackOut := editorWriter{&scrollbackEditor, true}
				fmt.Fprintf(&scrollbackOut, "Could not amend breakpoint: %v\n", err)
			}
		}(editedBreakpoint)
		editedBreakpoint = nil
		breakpointsPanel.clear()
		w.Close()
	}
}

func updateFuncs(mw *nucular.MasterWindow, w *nucular.Window) {
	updateStringSlice(mw, w, "functions", &funcsFilterEditor, functions)
}

func updateSources(mw *nucular.MasterWindow, w *nucular.Window) {
	updateStringSlice(mw, w, "sources", &sourcesFilterEditor, sources)
}

func updateTypes(mw *nucular.MasterWindow, w *nucular.Window) {
	updateStringSlice(mw, w, "types", &typesFilterEditor, types)
}

func updateStringSlice(mw *nucular.MasterWindow, container *nucular.Window, name string, filterEditor *nucular.TextEditor, values []string) {
	w := container.GroupBegin(name, 0)
	if w == nil {
		return
	}
	defer w.GroupEnd()

	w.MenubarBegin()
	w.Row(20).Static(90, 0)
	w.Label("Filter:", "LC")
	filterEditor.Edit(w)
	w.MenubarEnd()

	filter := string(filterEditor.Buffer)

	w.Row(20).Dynamic(1)
	for _, value := range values {
		if strings.Index(value, filter) >= 0 {
			w.Label(value, "LC")
		}
		// TODO: contextual menu with copy (but functions need to also have a "set breakpoint" entry)
	}
}

func showVariable(w *nucular.Window, depth int, addr bool, name string, v *api.Variable, ind int) {
	const minInlineKeyValueLen = 20
	if v.Type != "" {
		if addr {
			if name != "" {
				name = fmt.Sprintf("%#x %s %s", v.Addr, name, v.Type)
			} else {
				name = fmt.Sprintf("%#x %s", v.Addr, v.Type)
			}
		} else {
			if name != "" {
				name = fmt.Sprintf("%s %s", name, v.Type)
			} else {
				name = v.Type
			}
		}
	} else {
		if addr {
			if name != "" {
				name = fmt.Sprintf("%#x %s", v.Addr, name)
			} else {
				name = fmt.Sprintf("%#x", v.Addr)
			}
		}
	}
	if v.Unreadable != "" {
		w.Label(fmt.Sprintf("%s = (unreadable %s)", name, v.Unreadable), "LC")
		return
	}

	if depth > 0 && v.Addr == 0 {
		w.Label(fmt.Sprintf("%s = nil", name, v.Type), "LC")
		return
	}

	switch v.Kind {
	case reflect.Slice:
		if w.TreePush(nucular.TreeNode, name, false) {
			w.Scrollbar.X -= ind
			w.Label(fmt.Sprintf("len: %d cap: %d", v.Len, v.Cap), "LC")
			showArrayOrSliceContents(w, depth, addr, v, ind)
			w.Scrollbar.X += ind
			w.TreePop()
		}
	case reflect.Array:
		if w.TreePush(nucular.TreeNode, name, false) {
			w.Scrollbar.X -= ind
			w.Label(fmt.Sprintf("len: %d", v.Len), "LC")
			showArrayOrSliceContents(w, depth, addr, v, ind)
			w.Scrollbar.X += ind
			w.TreePop()
		}
	case reflect.Ptr:
		if v.Type == "" || v.Children[0].Addr == 0 {
			w.Label(fmt.Sprintf("%s = nil", name), "LC")
		} else if v.Children[0].OnlyAddr && v.Children[0].Addr != 0 {
			w.Label(fmt.Sprintf("%s = (%s)(%#x)", name, v.Type, v.Children[0].Addr), "LC")
		} else {
			if w.TreePush(nucular.TreeNode, name, false) {
				w.Scrollbar.X -= ind
				showVariable(w, depth+1, addr, "", &v.Children[0], ind)
				w.Scrollbar.X += ind
				w.TreePop()
			}
		}
	case reflect.UnsafePointer:
		w.Label(fmt.Sprintf("%s = unsafe.Pointer(%#x)", name, v.Children[0].Addr), "LC")
	case reflect.String:
		if len(v.Value) != int(v.Len) {
			w.Row(varRowHeight).Static(0, moreBtnWidth)
			w.Label(fmt.Sprintf("%s = %q", name, v.Value), "LC")
			w.Label(fmt.Sprintf("%d more", int(v.Len)-len(v.Value)), "LC")
			//TODO: detailed view for strings
			w.Row(varRowHeight).Dynamic(1)
		} else {
			w.Label(fmt.Sprintf("%s = %q", name, v.Value), "LC")
		}
	case reflect.Chan:
		if len(v.Children) == 0 {
			w.Label(fmt.Sprintf("%s = nil", name), "LC")
		} else {
			if w.TreePush(nucular.TreeNode, name, false) {
				w.Scrollbar.X -= ind
				showStructContents(w, depth, addr, v, ind)
				w.Scrollbar.X += ind
				w.TreePop()
			}
		}
	case reflect.Struct:
		if w.TreePush(nucular.TreeNode, name, false) {
			w.Scrollbar.X -= ind
			if int(v.Len) != len(v.Children) && len(v.Children) == 0 {
				loadMoreStruct(v)
				w.Label("Loading...", "LC")
			} else {
				showStructContents(w, depth, addr, v, ind)
			}
			w.Scrollbar.X += ind
			w.TreePop()
		}
	case reflect.Interface:
		if v.Children[0].Kind == reflect.Invalid {
			w.Label(fmt.Sprintf("%s = nil", name), "LC")
		} else {
			if w.TreePush(nucular.TreeNode, name, false) {
				w.Scrollbar.X -= ind
				if v.Children[0].Kind == reflect.Ptr {
					showVariable(w, depth+1, addr, "data", &v.Children[0].Children[0], ind)
				} else {
					showVariable(w, depth+1, addr, "data", &v.Children[0], ind)
				}
				w.Scrollbar.X += ind
				w.TreePop()
			}
		}
	case reflect.Map:
		if w.TreePush(nucular.TreeNode, name, false) {
			w.Scrollbar.X -= ind
			for i := 0; i < len(v.Children); i += 2 {
				key, value := &v.Children[i], &v.Children[i+1]
				if len(key.Children) == 0 && len(key.Value) < minInlineKeyValueLen {
					var keyname string
					if key.Kind == reflect.String {
						keyname = fmt.Sprintf("[%q]", key.Value)
					} else {
						keyname = fmt.Sprintf("[%s]", key.Value)
					}
					showVariable(w, depth+1, addr, keyname, value, ind)
				} else {
					showVariable(w, depth+1, addr, fmt.Sprintf("[%d key]", i/2), key, ind)
					showVariable(w, depth+1, addr, fmt.Sprintf("[%d value]", i/2), value, ind)
				}
			}
			if len(v.Children)/2 != int(v.Len) {
				w.Row(varRowHeight).Static(moreBtnWidth)
				if w.ButtonText(fmt.Sprintf("%d more", int(v.Len)-(len(v.Children)/2))) {
					loadMoreMap(v)
				}
				w.Row(varRowHeight).Dynamic(1)
			}
			w.Scrollbar.X += ind
			w.TreePop()
		}
	case reflect.Func:
		if v.Value == "" {
			w.Label(fmt.Sprintf("%s = nil", name), "LC")
		} else {
			w.Label(fmt.Sprintf("%s = %s", name, v.Value), "LC")
		}
	case reflect.Complex64, reflect.Complex128:
		w.Label(fmt.Sprintf("%s = (%s + %si)", name, v.Children[0].Value, v.Children[1].Value), "LC")
	default:
		if v.Value != "" {
			w.Label(fmt.Sprintf("%s = %s", name, v.Value), "LC")
		} else {
			w.Label(fmt.Sprintf("%s = (unknown %s)", name, v.Kind), "LC")
		}
	}
}

func showArrayOrSliceContents(w *nucular.Window, depth int, addr bool, v *api.Variable, ind int) {
	for i := range v.Children {
		showVariable(w, depth+1, addr, fmt.Sprintf("[%d]", i), &v.Children[i], ind)
	}
	if len(v.Children) != int(v.Len) {
		w.Row(varRowHeight).Static(moreBtnWidth)
		if w.ButtonText(fmt.Sprintf("%d more", int(v.Len)-len(v.Children))) {
			loadMoreArrayOrSlice(v)
		}
		w.Row(varRowHeight).Dynamic(1)
	}
}

func showStructContents(w *nucular.Window, depth int, addr bool, v *api.Variable, ind int) {
	for i := range v.Children {
		showVariable(w, depth+1, addr, v.Children[i].Name, &v.Children[i], ind)
	}
}

var additionalLoadMu sync.Mutex
var additionalLoadRunning bool

func loadMoreMap(v *api.Variable) {
	additionalLoadMu.Lock()
	defer additionalLoadMu.Unlock()

	if !additionalLoadRunning {
		additionalLoadRunning = true
		go func() {
			expr := fmt.Sprintf("(*(*%q)(%#x))[%d:]", v.Type, v.Addr, len(v.Children)/2)
			lv, err := client.EvalVariable(api.EvalScope{curGid, curFrame}, expr, LongLoadConfig)
			if err != nil {
				out := editorWriter{&scrollbackEditor, true}
				fmt.Fprintf(&out, "Error loading array contents %s: %v\n", expr, err)
				// prevent further attempts at loading
				v.Len = int64(len(v.Children) / 2)
			} else {
				v.Children = append(v.Children, lv.Children...)
			}
			wnd.Changed()
			additionalLoadMu.Lock()
			additionalLoadRunning = false
			additionalLoadMu.Unlock()
		}()
	}
}

func loadMoreArrayOrSlice(v *api.Variable) {
	additionalLoadMu.Lock()
	defer additionalLoadMu.Unlock()
	if !additionalLoadRunning {
		additionalLoadRunning = true
		go func() {
			expr := fmt.Sprintf("(*(*%q)(%#x))[%d:]", v.Type, v.Addr, len(v.Children))
			lv, err := client.EvalVariable(api.EvalScope{curGid, curFrame}, expr, LongLoadConfig)
			if err != nil {
				out := editorWriter{&scrollbackEditor, true}
				fmt.Fprintf(&out, "Error loading array contents %s: %v\n", expr, err)
				// prevent further attempts at loading
				v.Len = int64(len(v.Children))
			} else {
				v.Children = append(v.Children, lv.Children...)
			}
			additionalLoadMu.Lock()
			additionalLoadRunning = false
			additionalLoadMu.Unlock()
			wnd.Changed()
		}()
	}
}

func loadMoreStruct(v *api.Variable) {
	additionalLoadMu.Lock()
	defer additionalLoadMu.Unlock()
	if !additionalLoadRunning {
		additionalLoadRunning = true
		go func() {
			lv, err := client.EvalVariable(api.EvalScope{curGid, curFrame}, fmt.Sprintf("*(*%q)(%#x)", v.Type, v.Addr), LongLoadConfig)
			if err != nil {
				v.Unreadable = err.Error()
			} else {
				lv.Name = v.Name
				*v = *lv
			}
			wnd.Changed()
			additionalLoadMu.Lock()
			additionalLoadRunning = false
			additionalLoadMu.Unlock()
		}()
	}
}
