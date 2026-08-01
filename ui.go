package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// ui is the native (Fyne) frontend. It subscribes to the event hub and
// renders the inbox / send / settings panes.
type ui struct {
	a   fyne.App
	win fyne.Window
	cfg *config
	srv *server
	ctx context.Context

	mu       sync.Mutex
	inbox    []waitingFile
	devices  []device
	saving   map[string]*saveState
	sends    map[string]*sendState
	daemonOK bool

	// widgets
	status     *widget.Label
	inboxBox   *fyne.Container
	inboxRows  map[string]*inboxRowWidget
	emptyLabel *widget.Label
	saveAllBtn *widget.Button
	devSelect  *widget.Select
	devicesMap map[string]device // select label -> device
	sendBox    *fyne.Container
	saveDirLbl *widget.Label
	autoCheck  *widget.Check
	trayAuto   *fyne.MenuItem
	trayMenu   *fyne.Menu
}

type saveState struct {
	written, size int64
	done          bool
	path, err     string
}

type sendState struct {
	peerID, peerName string
	name             string
	sent, size       int64
	done             bool
	err              string
}

type inboxRowWidget struct {
	sub      *widget.Label
	bar      *widget.ProgressBar
	barLabel *widget.Label
	actions  *fyne.Container
	box      *fyne.Container
}

func newUI(a fyne.App, cfg *config, srv *server, ctx context.Context) *ui {
	u := &ui{
		a:         a,
		cfg:       cfg,
		srv:       srv,
		ctx:       ctx,
		saving:    map[string]*saveState{},
		sends:     map[string]*sendState{},
		devicesMap: map[string]device{},
		inboxRows: map[string]*inboxRowWidget{},
	}
	u.build()
	return u
}

func (u *ui) build() {
	u.win = u.a.NewWindow("Taildrop")
	u.win.Resize(fyne.NewSize(880, 620))

	// --- header ---
	u.status = widget.NewLabelWithStyle("connecting to tailscaled…", fyne.TextAlignLeading, fyne.TextStyle{})
	u.status.Importance = widget.WarningImportance
	header := container.NewHBox(
		widget.NewIcon(theme.MailComposeIcon()),
		widget.NewLabelWithStyle("Taildrop", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		widget.NewSeparator(),
		u.status,
	)

	// --- inbox tab ---
	u.emptyLabel = widget.NewLabel("No files waiting.\nWhen someone on your tailnet sends you a file, it appears here.")
	u.emptyLabel.Alignment = fyne.TextAlignCenter
	u.emptyLabel.TextStyle = fyne.TextStyle{Italic: true}
	u.emptyLabel.Wrapping = fyne.TextWrapWord
	u.inboxBox = container.NewVBox()
	inboxScroll := container.NewVScroll(container.NewStack(u.emptyLabel, u.inboxBox))
	inboxScroll.SetMinSize(fyne.NewSize(0, 420))

	u.saveAllBtn = widget.NewButtonWithIcon("Save all", theme.DownloadIcon(), u.saveAll)
	toolbar := container.NewHBox(
		u.saveAllBtn,
		widget.NewLabelWithStyle("Inbox: files sent to this machine", fyne.TextAlignLeading, fyne.TextStyle{}),
	)
	inboxTab := container.NewBorder(toolbar, nil, nil, nil, inboxScroll)

	// --- send tab ---
	u.devSelect = widget.NewSelect(nil, nil)
	refreshBtn := widget.NewButtonWithIcon("Refresh", theme.ViewRefreshIcon(), u.refreshDevices)
	chooseBtn := widget.NewButtonWithIcon("Choose file…", theme.FolderOpenIcon(), u.chooseFiles)
	dropHint := widget.NewLabel("…or drag & drop files onto this window to send them.")
	dropHint.TextStyle = fyne.TextStyle{Italic: true}
	dropHint.Alignment = fyne.TextAlignCenter
	u.sendBox = container.NewVBox()
	sendScroll := container.NewVScroll(u.sendBox)
	sendScroll.SetMinSize(fyne.NewSize(0, 380))
	sendTop := container.NewBorder(
		container.NewHBox(widget.NewLabel("Send to"), u.devSelect, refreshBtn, chooseBtn),
		nil, nil, nil,
		container.NewVBox(dropHint, sendScroll),
	)
	sendTab := container.NewBorder(sendTop, nil, nil, nil, container.NewStack(sendScroll))

	// --- settings tab ---
	u.saveDirLbl = widget.NewLabel(u.cfg.SaveDir)
	changeDirBtn := widget.NewButton("Change…", u.pickSaveDir)
	u.autoCheck = widget.NewCheck("Automatically save incoming files to the folder above", func(on bool) {
		u.cfg.AutoSave = on
		u.cfg.save()
		u.updateAutoMenu()
	})
	u.autoCheck.SetChecked(u.cfg.AutoSave)
	dirRow := container.NewHBox(widget.NewLabelWithStyle("Save folder", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}), u.saveDirLbl, changeDirBtn)
	settings := container.NewVBox(
		dirRow,
		widget.NewSeparator(),
		u.autoCheck,
		widget.NewLabelWithStyle("Auto-save works like `tailscale file get --loop`: files land in the folder above as soon as they arrive, with a notification.", fyne.TextAlignLeading, fyne.TextStyle{Italic: true}),
		widget.NewSeparator(),
		widget.NewLabel("Taildrop UI — talks directly to your local tailscaled daemon. Nothing leaves this machine except files you send."),
	)

	tabs := container.NewAppTabs(
		container.NewTabItemWithIcon("Inbox", theme.DownloadIcon(), inboxTab),
		container.NewTabItemWithIcon("Send", theme.UploadIcon(), sendTab),
		container.NewTabItemWithIcon("Settings", theme.SettingsIcon(), settings),
	)
	tabs.OnSelected = func(ti *container.TabItem) {
		if ti.Text == "Send" {
			u.refreshDevices()
		}
	}

	u.win.SetContent(container.NewBorder(header, nil, nil, nil, tabs))

	// close = hide to tray; quit via the tray menu
	u.win.SetCloseIntercept(func() {
		if d, ok := u.a.(desktop.App); ok {
			d.SetSystemTrayWindow(u.win)
			u.win.Hide()
		} else {
			u.a.Quit()
		}
	})

	// drag & drop files onto the window to send them
	u.win.SetOnDropped(func(_ fyne.Position, uris []fyne.URI) {
		for _, uri := range uris {
			if uri != nil && uri.Path() != "" {
				u.sendPath(uri.Path())
			}
		}
	})

	u.setupTray()
}

func (u *ui) setupTray() {
	d, ok := u.a.(desktop.App)
	if !ok {
		return
	}
	u.trayAuto = fyne.NewMenuItem("", func() {
		u.cfg.AutoSave = !u.cfg.AutoSave
		u.cfg.save()
		u.autoCheck.SetChecked(u.cfg.AutoSave)
		u.updateAutoMenu()
	})
	quit := fyne.NewMenuItem("Quit Taildrop", u.a.Quit)
	u.trayMenu = fyne.NewMenu("Taildrop",
		fyne.NewMenuItem("Show", func() {
			u.win.Show()
			u.win.RequestFocus()
		}),
		u.trayAuto,
		fyne.NewMenuItemSeparator(),
		quit,
	)
	u.updateAutoMenu()
	d.SetSystemTrayMenu(u.trayMenu)
	d.SetSystemTrayWindow(u.win)
}

func (u *ui) updateAutoMenu() {
	if u.trayAuto == nil {
		return
	}
	state := "off"
	if u.cfg.AutoSave {
		state = "on"
	}
	u.trayAuto.Label = fmt.Sprintf("Auto-save: %s", state)
	// Rebuild the menu so the label refreshes (Fyne menus are immutable).
	d, ok := u.a.(desktop.App)
	if !ok {
		return
	}
	d.SetSystemTrayMenu(u.trayMenu)
}

// run starts the app: initial state fetch, event subscription, window loop.
func (u *ui) run() {
	// Initial inbox snapshot (the watcher's long-poll can sit idle for 30s).
	if files, err := tsInbox(u.ctx); err == nil {
		u.srv.hub.broadcast(inboxEvent{Type: "inbox", Files: files})
	} else {
		u.setDaemon(false, err.Error())
	}
	go u.refreshDevices()

	// Subscribe to hub events.
	ch := u.srv.hub.subscribeNative()
	go func() {
		defer u.srv.hub.unsubscribeNative(ch)
		for {
			select {
			case <-u.ctx.Done():
				return
			case ev, ok := <-ch:
				if !ok {
					return
				}
				u.handleHubEvent(ev)
			}
		}
	}()

	// Refresh relative timestamps every 30s.
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-u.ctx.Done():
				return
			case <-t.C:
				fyne.Do(func() { u.refreshAges() })
			}
		}
	}()

	u.win.ShowAndRun()
}

func (u *ui) handleHubEvent(ev any) {
	switch e := ev.(type) {
	case inboxEvent:
		u.mu.Lock()
		known := map[string]bool{}
		for _, f := range u.inbox {
			known[f.Name] = true
		}
		u.inbox = e.Files
		fresh := []waitingFile{}
		for _, f := range e.Files {
			if !known[f.Name] {
				fresh = append(fresh, f)
			}
		}
		u.mu.Unlock()
		fyne.Do(func() { u.renderInbox() })
		for _, f := range fresh {
			u.onFileArrived(f)
		}
	case devicesEvent:
		u.mu.Lock()
		u.devices = e.Devices
		u.mu.Unlock()
		fyne.Do(func() { u.renderDevices() })
	case saveEvent:
		u.mu.Lock()
		st := u.saving[e.Name]
		if st != nil {
			st.written = e.Written
			st.size = e.Size
			if e.Done {
				st.done = true
				st.path = e.Path
				st.err = e.Err
			}
		}
		u.mu.Unlock()
		if st != nil {
			fyne.Do(func() { u.updateSaveRow(e.Name) })
		}
	case sendEvent:
		u.mu.Lock()
		st := u.sends[e.ID]
		if st != nil {
			st.sent = e.Sent
			if e.Size >= 0 {
				st.size = e.Size
			}
			if e.Done {
				st.done = true
				st.err = e.Err
			}
		}
		u.mu.Unlock()
		if st != nil {
			fyne.Do(func() { u.updateSendRow(e.ID) })
		}
	case statusEvent:
		u.setDaemon(false, e.Err)
	}
}

func (u *ui) setDaemon(ok bool, errMsg string) {
	u.mu.Lock()
	u.daemonOK = ok
	u.mu.Unlock()
	fyne.Do(func() {
		if ok {
			u.status.Text = "connected to tailscaled"
			u.status.Importance = widget.SuccessImportance
			u.saveAllBtn.Enable()
		} else {
			u.status.Text = "tailscaled unreachable: " + errMsg
			u.status.Importance = widget.DangerImportance
			u.saveAllBtn.Disable()
		}
		u.status.Refresh()
	})
}

// --- inbox ---------------------------------------------------------------

func (u *ui) renderInbox() {
	u.mu.Lock()
	files := append([]waitingFile(nil), u.inbox...)
	saving := map[string]*saveState{}
	for k, v := range u.saving {
		saving[k] = v
	}
	u.mu.Unlock()

	u.inboxBox.Objects = nil
	u.inboxRows = map[string]*inboxRowWidget{}
	u.emptyLabel.Hidden = len(files) > 0

	for _, f := range files {
		row := u.newInboxRow(f, saving[f.Name])
		u.inboxRows[f.Name] = row
		u.inboxBox.Add(row.box)
	}
	u.inboxBox.Refresh()
	if len(files) > 0 {
		u.saveAllBtn.Enable()
	} else {
		u.saveAllBtn.Disable()
	}
}

func (u *ui) newInboxRow(f waitingFile, st *saveState) *inboxRowWidget {
	name := widget.NewLabelWithStyle(f.Name, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	name.Truncation = fyne.TextTruncateEllipsis
	name.Wrapping = fyne.TextWrapWord
	sub := widget.NewLabel(fmtSize(f.Size) + " · " + fmtAge(f.Arrived))
	sub.TextStyle = fyne.TextStyle{}
	bar := widget.NewProgressBar()
	bar.Hide()
	barLabel := widget.NewLabel("")
	barLabel.Hide()

	saveBtn := widget.NewButtonWithIcon("Save", theme.DownloadIcon(), func() {
		u.saveFile(f.Name, f.Size, u.cfg.SaveDir)
	})
	saveToBtn := widget.NewButton("Save to…", func() {
		d := dialog.NewFolderOpen(func(lu fyne.ListableURI, err error) {
			if err != nil || lu == nil {
				return
			}
			u.saveFile(f.Name, f.Size, lu.Path())
		}, u.win)
		d.Show()
	})
	delBtn := widget.NewButtonWithIcon("Delete", theme.DeleteIcon(), func() {
		u.deleteFile(f.Name)
	})
	actions := container.NewHBox(saveBtn, saveToBtn, delBtn)

	row := &inboxRowWidget{
		sub:      sub,
		bar:      bar,
		barLabel: barLabel,
		actions:  actions,
	}
	row.box = container.NewVBox(
		name,
		sub,
		container.NewStack(
			actions,
			container.NewHBox(bar, barLabel),
		),
		widget.NewSeparator(),
	)

	if st != nil && !st.done {
		u.applySaveState(row, st)
	}
	return row
}

func (u *ui) applySaveState(row *inboxRowWidget, st *saveState) {
	if st == nil || st.done {
		row.bar.Hide()
		row.barLabel.Hide()
		row.actions.Show()
		return
	}
	row.actions.Hide()
	row.bar.Show()
	row.barLabel.Show()
	if st.size > 0 {
		row.bar.SetValue(float64(st.written) / float64(st.size))
		row.barLabel.SetText(fmt.Sprintf("%s / %s (%d%%)", fmtSize(st.written), fmtSize(st.size), 100*st.written/st.size))
	} else {
		row.bar.SetValue(0)
		row.barLabel.SetText(fmtSize(st.written))
	}
}

func (u *ui) updateSaveRow(name string) {
	row, ok := u.inboxRows[name]
	if !ok {
		return
	}
	u.mu.Lock()
	st := u.saving[name]
	u.mu.Unlock()
	if st != nil {
		u.applySaveState(row, st)
		row.box.Refresh()
	}
}

func (u *ui) refreshAges() {
	u.mu.Lock()
	defer u.mu.Unlock()
	for _, f := range u.inbox {
		if row, ok := u.inboxRows[f.Name]; ok {
			row.sub.SetText(fmtSize(f.Size) + " · " + fmtAge(f.Arrived))
		}
	}
}

func (u *ui) saveFile(name string, size int64, dir string) {
	u.mu.Lock()
	if _, busy := u.saving[name]; busy {
		u.mu.Unlock()
		u.notify("Taildrop", "Already saving "+name)
		return
	}
	u.saving[name] = &saveState{size: size}
	u.mu.Unlock()
	fyne.Do(func() { u.renderInbox() })

	go func() {
		path, err := u.srv.saveOne(u.ctx, name, dir, nil)
		u.mu.Lock()
		st := u.saving[name]
		if st != nil {
			st.done = true
			st.path = path
			st.err = errString(err)
		}
		u.mu.Unlock()
		fyne.Do(func() { u.renderInbox() })
		if err != nil {
			u.notify("Taildrop: save failed", name+": "+err.Error())
			// File stays in the inbox; allow a retry.
			u.mu.Lock()
			delete(u.saving, name)
			u.mu.Unlock()
			fyne.Do(func() { u.renderInbox() })
			return
		}
		u.notify("Taildrop: saved", name+" → "+path)
		// The daemon inbox update removes the row; drop the state shortly after.
		time.AfterFunc(5*time.Second, func() {
			u.mu.Lock()
			delete(u.saving, name)
			u.mu.Unlock()
		})
	}()
}

func (u *ui) saveAll() {
	u.mu.Lock()
	files := append([]waitingFile(nil), u.inbox...)
	u.mu.Unlock()
	for _, f := range files {
		u.saveFile(f.Name, f.Size, u.cfg.SaveDir)
	}
}

func (u *ui) deleteFile(name string) {
	go func() {
		if err := u.srv.deleteInboxFile(u.ctx, name); err != nil {
			u.notify("Taildrop", "Couldn't delete "+name+": "+err.Error())
		}
	}()
}

// onFileArrived handles a newly noticed inbox file: auto-save or notify.
func (u *ui) onFileArrived(f waitingFile) {
	u.mu.Lock()
	auto := u.cfg.AutoSave && u.daemonOK
	busy := u.saving[f.Name] != nil
	u.mu.Unlock()
	if auto && !busy {
		u.saveFile(f.Name, f.Size, u.cfg.SaveDir)
		// saveFile already notifies on completion; announce the arrival too.
		u.notify("Taildrop: new file", f.Name+" ("+fmtSize(f.Size)+") — auto-saving to "+u.cfg.SaveDir)
	} else if !auto {
		u.notify("Taildrop: new file", f.Name+" ("+fmtSize(f.Size)+") — open Taildrop to save it")
	}
}

func (u *ui) notify(title, content string) {
	u.a.SendNotification(fyne.NewNotification(title, content))
}

// --- send ----------------------------------------------------------------

func (u *ui) refreshDevices() {
	devs, err := tsDevices(u.ctx)
	if err != nil {
		u.setDaemon(false, err.Error())
		return
	}
	u.mu.Lock()
	u.devices = devs
	u.mu.Unlock()
	fyne.Do(func() { u.renderDevices() })
	u.setDaemon(true, "")
}

func deviceLabel(d device) string {
	s := d.Name
	if d.OS != "" {
		s += " (" + d.OS + ")"
	}
	if !d.Online {
		s += " — offline"
	}
	if d.Taildrop != "available" {
		s += " — " + d.Taildrop
	}
	return s
}

func (u *ui) renderDevices() {
	u.mu.Lock()
	devs := append([]device(nil), u.devices...)
	u.mu.Unlock()

	labels := make([]string, 0, len(devs))
	byLabel := map[string]device{}
	for _, d := range devs {
		l := deviceLabel(d)
		labels = append(labels, l)
		byLabel[l] = d
	}
	if len(labels) == 0 {
		return
	}

	oldValue := u.devSelect.Selected
	oldLabels := u.devSelect.Options
	same := len(oldLabels) == len(labels)
	if same {
		for i := range labels {
			if oldLabels[i] != labels[i] {
				same = false
				break
			}
		}
	}
	u.devicesMap = byLabel
	u.devSelect.Options = labels
	if !same || oldValue == "" {
		u.devSelect.SetSelected(labels[0])
	} else if oldValue != "" {
		u.devSelect.SetSelected(oldValue)
	}
}

func (u *ui) chooseFiles() {
	d := dialog.NewFileOpen(func(rc fyne.URIReadCloser, err error) {
		if err != nil || rc == nil {
			return
		}
		path := rc.URI().Path()
		rc.Close()
		u.sendPath(path)
	}, u.win)
	d.Show()
}

func (u *ui) sendPath(path string) {
	u.mu.Lock()
	dev, ok := u.devicesMap[u.devSelect.Selected]
	daemonOK := u.daemonOK
	u.mu.Unlock()
	if !ok {
		u.notify("Taildrop", "Choose a device to send to first")
		return
	}
	if !daemonOK {
		u.notify("Taildrop", "tailscaled is not reachable")
		return
	}
	f, err := os.Open(path)
	if err != nil {
		u.notify("Taildrop", "Can't open "+path+": "+err.Error())
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		u.notify("Taildrop", err.Error())
		return
	}
	if fi.IsDir() {
		u.notify("Taildrop", "Taildrop doesn't support directories — send a file instead")
		return
	}
	name := filepath.Base(path)
	id := fmt.Sprintf("%d", time.Now().UnixNano())
	u.mu.Lock()
	u.sends[id] = &sendState{peerID: string(dev.ID), peerName: dev.Name, name: name, size: fi.Size()}
	u.mu.Unlock()
	fyne.Do(func() { u.renderSends() })

	go func() {
		err := u.srv.sendOne(u.ctx, id, dev.ID, name, fi.Size(), f, nil)
		u.mu.Lock()
		if st := u.sends[id]; st != nil {
			st.done = true
			if err != nil {
				st.err = err.Error()
			}
		}
		u.mu.Unlock()
		fyne.Do(func() { u.updateSendRow(id) })
		if err != nil {
			u.notify("Taildrop: send failed", name+" → "+dev.Name+": "+err.Error())
			return
		}
		u.notify("Taildrop: sent", name+" → "+dev.Name)
		time.AfterFunc(10*time.Second, func() {
			u.mu.Lock()
			delete(u.sends, id)
			u.mu.Unlock()
			fyne.Do(func() { u.renderSends() })
		})
	}()
}

func (u *ui) renderSends() {
	u.mu.Lock()
	sends := make([]*sendState, 0, len(u.sends))
	ids := make([]string, 0, len(u.sends))
	for id, st := range u.sends {
		ids = append(ids, id)
		sends = append(sends, st)
	}
	u.mu.Unlock()

	sort.Slice(sends, func(i, j int) bool { return ids[i] < ids[j] })

	sendRow := func(st *sendState) *fyne.Container {
		name := widget.NewLabelWithStyle(st.name, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
		name.Truncation = fyne.TextTruncateEllipsis
		sub := widget.NewLabel("sending to " + st.peerName + "…")
		bar := widget.NewProgressBar()
		barLabel := widget.NewLabel("")
		if st.done {
			if st.err != "" {
				sub.SetText("failed: " + st.err)
				bar.Hide()
				barLabel.Hide()
			} else {
				sub.SetText("sent to " + st.peerName)
				bar.SetValue(1)
				barLabel.SetText("done")
			}
		} else if st.size > 0 {
			bar.SetValue(float64(st.sent) / float64(st.size))
			barLabel.SetText(fmt.Sprintf("%s / %s (%d%%)", fmtSize(st.sent), fmtSize(st.size), 100*st.sent/st.size))
		} else {
			barLabel.SetText(fmtSize(st.sent))
		}
		return container.NewVBox(
			name,
			sub,
			container.NewHBox(bar, barLabel),
			widget.NewSeparator(),
		)
	}
	u.sendBox.Objects = nil
	if len(sends) == 0 {
		u.sendBox.Refresh()
		return
	}
	for _, st := range sends {
		u.sendBox.Add(sendRow(st))
	}
	u.sendBox.Refresh()
}

func (u *ui) updateSendRow(id string) {
	// Simpler to rebuild: send rows are few and ephemeral.
	u.renderSends()
}

func (u *ui) pickSaveDir() {
	d := dialog.NewFolderOpen(func(lu fyne.ListableURI, err error) {
		if err != nil || lu == nil {
			return
		}
		dir := lu.Path()
		u.cfg.SaveDir = dir
		u.cfg.save()
		u.saveDirLbl.SetText(dir)
	}, u.win)
	d.Show()
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// fmtSize renders a byte count as 1.5 MiB etc.
func fmtSize(n int64) string {
	if n < 0 {
		return "?"
	}
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	units := []string{"KiB", "MiB", "GiB", "TiB"}
	v := float64(n)
	i := -1
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	if v >= 100 {
		return fmt.Sprintf("%.0f %s", v, units[i])
	}
	return fmt.Sprintf("%.1f %s", v, units[i])
}

// fmtAge renders a time as "just now", "3m ago", "2h ago", …
func fmtAge(t time.Time) string {
	s := time.Since(t).Seconds()
	switch {
	case s < 10:
		return "just now"
	case s < 60:
		return fmt.Sprintf("%.0fs ago", s)
	case s < 3600:
		return fmt.Sprintf("%.0fm ago", s/60)
	case s < 86400:
		return fmt.Sprintf("%.0fh ago", s/3600)
	default:
		return fmt.Sprintf("%.0fd ago", s/86400)
	}
}
