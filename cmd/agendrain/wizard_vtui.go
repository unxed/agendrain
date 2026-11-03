package main

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"unicode"

	"github.com/unxed/vtinput"
	"github.com/unxed/vtui"
	"golang.org/x/term"
)

// vtuiWizard owns one terminal session; each prompt only changes its modal
// dialog. Keeping the reader alive preserves input between wizard steps.
type vtuiWizard struct {
	fm *vtui.FrameManagerType
}

type wizardModal struct {
	*vtui.Window
	onHelp  func()
	onFocus func()
}

func (d *wizardModal) ProcessKey(e *vtinput.InputEvent) bool {
	if e.KeyDown && !e.TextInput {
		if e.VirtualKeyCode == vtinput.VK_F1 && d.onHelp != nil {
			d.onHelp()
			return true
		}
		ctrl := e.ControlKeyState&(vtinput.LeftCtrlPressed|vtinput.RightCtrlPressed) != 0
		if ctrl && (e.VirtualKeyCode == 'C' || e.Char == 'c' || e.Char == 3) {
			d.Close()
			return true
		}
	}
	handled := d.Window.ProcessKey(e)
	if d.onFocus != nil {
		d.onFocus()
	}
	return handled
}

func (d *wizardModal) ProcessMouse(e *vtinput.InputEvent) bool {
	handled := d.Window.ProcessMouse(e)
	if d.onFocus != nil {
		d.onFocus()
	}
	return handled
}

func newVTUIWizard() (wizardUI, func(), error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		return nil, nil, fmt.Errorf("интерактивный мастер требует терминал; используйте agendrain --help для командной строки")
	}
	width, height, err := term.GetSize(int(os.Stdin.Fd()))
	if err != nil {
		return nil, nil, fmt.Errorf("размер терминала: %w", err)
	}
	if width < 60 || height < 20 {
		return nil, nil, fmt.Errorf("для мастера нужен терминал не меньше 60×20 (сейчас %d×%d)", width, height)
	}
	restore, err := vtui.PrepareTerminal()
	if err != nil {
		return nil, nil, fmt.Errorf("подготовка терминала: %w", err)
	}
	previous := vtui.FrameManager
	fm := vtui.NewFrameManager()
	vtui.FrameManager = fm
	screen := vtui.NewScreenBuf()
	if vtui.DefaultConsoleBackend() == "winapi" {
		screen.Renderer = vtui.NewWin32ConsoleRenderer(screen)
	}
	screen.AllocBuf(width, height)
	fm.Init(screen)
	fm.Push(vtui.NewDesktop())
	vtinput.DrainInput()
	reader := vtinput.NewReader(os.Stdin, false)
	fm.Reader = reader
	fm.EventChan = reader.GetEventChan()
	var once sync.Once
	cleanup := func() {
		once.Do(func() {
			reader.Close()
			fm.Shutdown()
			restore()
			vtui.FrameManager = previous
		})
	}
	return &vtuiWizard{fm: fm}, cleanup, nil
}

// show uses vtui's normal dispatch/render cycle without repeatedly starting
// and closing its input reader. The desktop stays underneath all prompts.
func (u *vtuiWizard) show(dialog *wizardModal) error {
	u.fm.Push(dialog)
	if dialog.onFocus != nil {
		dialog.onFocus()
	}
	for !dialog.IsDone() {
		if !u.fm.Step(-1) {
			return errWizardCanceled
		}
	}
	if dialog.ExitCode != 1 {
		return errWizardCanceled
	}
	return nil
}

func (u *vtuiWizard) Select(title, help string, choices []wizardChoice) (int, error) {
	if len(choices) == 0 {
		return 0, fmt.Errorf("нет доступных вариантов")
	}
	dialog, list := u.selectDialog(title, help, choices)
	if err := u.show(dialog); err != nil {
		return 0, err
	}
	return list.SelectPos, nil
}

func (u *vtuiWizard) Options(title string, options []wizardOption) ([]wizardOption, error) {
	dialog, boxes := u.optionsDialog(title, options)
	if err := u.show(dialog); err != nil {
		return nil, err
	}
	result := append([]wizardOption(nil), options...)
	for i, box := range boxes {
		result[i].Checked = box.State == 1
	}
	return result, nil
}

func (u *vtuiWizard) Input(title, label, initial string) (string, error) {
	dialog, edit := u.inputDialog(title, label, initial)
	if err := u.show(dialog); err != nil {
		return "", err
	}
	return edit.GetText(), nil
}

func (u *vtuiWizard) Message(title, text string) error {
	return u.show(u.messageDialog(title, text))
}

func (u *vtuiWizard) width() int {
	return min(76, u.fm.Screen().Width()-2)
}

func (u *vtuiWizard) height() int {
	return min(22, u.fm.Screen().Height()-2)
}

func (u *vtuiWizard) modal(title string, width, height int) *wizardModal {
	dialog := &wizardModal{Window: vtui.NewCenteredDialog(width, height, " agendrain · "+wizardVisibleText(title)+" ")}
	dialog.ShowClose = true
	dialog.OutsideClickIgnored = true
	return dialog
}

func wizardVisibleText(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
}

func wizardCaption(s string) string {
	return strings.ReplaceAll(wizardVisibleText(s), "&", "&&")
}

func wizardLabel(dialog *wizardModal, x, y, width int, text string) *vtui.Text {
	label := vtui.NewText(x, y, wizardCaption(vtui.TruncateString(wizardVisibleText(text), width, "…")), 0)
	label.SetPosition(x, y, x+width-1, y)
	dialog.AddItem(label)
	return label
}

func wizardDetail(labels []*vtui.Text, text string, width int) {
	lines := vtui.WrapText(wizardVisibleText(text), width)
	for i, label := range labels {
		line := ""
		if i < len(lines) {
			line = lines[i]
		}
		if i == len(labels)-1 && len(lines) > len(labels) {
			line = vtui.TruncateString(line, width-1, "") + "…"
		}
		label.SetText(wizardCaption(line))
	}
}

func wizardButtons(dialog *wizardModal, y int, accept string, cancel bool) {
	width := dialog.X2 - dialog.X1 + 1
	ok := vtui.NewButton(0, y, accept)
	ok.IsDefault = true
	ok.OnClick = func() { dialog.SetExitCode(1) }
	buttonWidth := ok.X2 - ok.X1 + 1
	var back *vtui.Button
	if cancel {
		back = vtui.NewButton(0, y, "Отмена")
		back.OnClick = func() { dialog.Close() }
		buttonWidth += 3 + back.X2 - back.X1 + 1
	}
	x := dialog.X1 + (width-buttonWidth)/2
	ok.MoveRelative(x-ok.X1, 0)
	dialog.AddItem(ok)
	if back != nil {
		back.MoveRelative(ok.X2+4-back.X1, 0)
		dialog.AddItem(back)
	}
}

func (u *vtuiWizard) help(dialog *wizardModal, text string) {
	dialog.onHelp = func() {
		u.fm.Push(u.messageDialog("Справка", text+"\n\nСтрелки — выбор. Tab — переход к кнопкам. Enter — продолжить. Пробел — переключить флажок. Esc — отмена. Ctrl+C — выход."))
	}
}

func (u *vtuiWizard) selectDialog(title, help string, choices []wizardChoice) (*wizardModal, *vtui.ListBox) {
	width, height := u.width(), u.height()
	dialog := u.modal(title, width, height)
	x, y, inner := dialog.X1+2, dialog.Y1, width-4
	wizardLabel(dialog, x, y+2, inner, help)
	items := make([]string, len(choices))
	for i, choice := range choices {
		items[i] = wizardVisibleText(choice.Title)
	}
	list := vtui.NewListBox(x, y+4, inner, height-14, items)
	dialog.AddItem(list)
	details := make([]*vtui.Text, 3)
	for i := range details {
		details[i] = wizardLabel(dialog, x, y+height-9+i, inner, "")
	}
	list.OnSelect = func(index int) {
		if index >= 0 && index < len(choices) {
			wizardDetail(details, choices[index].Detail, inner)
		}
	}
	list.OnAction = func(int) { dialog.SetExitCode(1) }
	list.SetSelectPos(0)
	if len(choices) > 0 {
		list.OnSelect(0)
	}
	wizardLabel(dialog, x, y+height-5, inner, "↑↓ — выбор · Enter — далее · Esc — отмена · F1 — справка")
	wizardButtons(dialog, y+height-3, "Далее", true)
	u.help(dialog, help)
	return dialog, list
}

func (u *vtuiWizard) optionsDialog(title string, options []wizardOption) (*wizardModal, []*vtui.Checkbox) {
	width := u.width()
	height := max(12, len(options)+10)
	dialog := u.modal(title, width, height)
	x, y, inner := dialog.X1+2, dialog.Y1, width-4
	wizardLabel(dialog, x, y+2, inner, "↑↓ / Tab — переход · Пробел — флажок · F1 — справка")
	boxes := make([]*vtui.Checkbox, len(options))
	for i, option := range options {
		label := vtui.TruncateString(wizardVisibleText(option.Label), inner-4, "…")
		box := vtui.NewCheckbox(x, y+4+i, wizardCaption(label), false)
		if option.Checked {
			box.State = 1
		}
		dialog.AddItem(box)
		boxes[i] = box
	}
	details := []*vtui.Text{
		wizardLabel(dialog, x, y+height-6, inner, ""),
		wizardLabel(dialog, x, y+height-5, inner, ""),
	}
	dialog.onFocus = func() {
		for i, box := range boxes {
			if dialog.GetFocusedItem() == box {
				wizardDetail(details, options[i].Detail, inner)
				return
			}
		}
	}
	wizardButtons(dialog, y+height-3, "Далее", true)
	var help strings.Builder
	for _, option := range options {
		fmt.Fprintf(&help, "%s: %s\n\n", option.Label, option.Detail)
	}
	u.help(dialog, help.String())
	return dialog, boxes
}

func (u *vtuiWizard) inputDialog(title, label, initial string) (*wizardModal, *vtui.Edit) {
	width := u.width()
	dialog := u.modal(title, width, 13)
	x, y, inner := dialog.X1+2, dialog.Y1, width-4
	labels := []*vtui.Text{wizardLabel(dialog, x, y+2, inner, ""), wizardLabel(dialog, x, y+3, inner, "")}
	wizardDetail(labels, label, inner)
	edit := vtui.NewEdit(x, y+5, inner, initial)
	dialog.AddItem(edit)
	wizardLabel(dialog, x, y+7, inner, "Enter — сохранить значение · Esc — отмена · F1 — справка")
	wizardButtons(dialog, y+10, "Далее", true)
	u.help(dialog, label)
	return dialog, edit
}

func (u *vtuiWizard) messageDialog(title, text string) *wizardModal {
	width, height := u.width(), u.height()
	dialog := u.modal(title, width, height)
	x, y, inner := dialog.X1+2, dialog.Y1, width-4
	var lines []string
	for _, paragraph := range strings.Split(text, "\n") {
		wrapped := vtui.WrapText(wizardVisibleText(paragraph), inner-2)
		if len(wrapped) == 0 {
			wrapped = []string{""}
		}
		lines = append(lines, wrapped...)
	}
	list := vtui.NewListBox(x, y+2, inner, height-9, lines)
	list.OnAction = func(int) { dialog.SetExitCode(1) }
	dialog.AddItem(list)
	wizardLabel(dialog, x, y+height-5, inner, "↑↓ / PgUp / PgDn — прокрутка · Enter — закрыть")
	wizardButtons(dialog, y+height-3, "Закрыть", false)
	return dialog
}
