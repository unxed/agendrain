package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/unxed/vtinput"
	"github.com/unxed/vtui"
)

func silentWizard(t *testing.T) *vtuiWizard {
	t.Helper()
	previous := vtui.FrameManager
	fm := vtui.NewFrameManager()
	vtui.FrameManager = fm
	screen := vtui.NewSilentScreenBuf()
	screen.AllocBuf(80, 25)
	fm.Init(screen)
	fm.Push(vtui.NewDesktop())
	t.Cleanup(func() { fm.Shutdown(); vtui.FrameManager = previous })
	return &vtuiWizard{fm: fm}
}

func wizardKey(key uint16) *vtinput.InputEvent {
	return &vtinput.InputEvent{Type: vtinput.KeyEventType, KeyDown: true, VirtualKeyCode: key}
}

func TestVTUIWizardSelectKeyboardAndLayout(t *testing.T) {
	u := silentWizard(t)
	dialog, list := u.selectDialog("Выбор диалога", "Выберите работу для переноса", []wizardChoice{
		{Title: "Первый диалог", Detail: "Требования и первоначальное решение"},
		{Title: "Второй диалог", Detail: "Продолжение работы · /tmp/project"},
	})
	vtui.AssertLayout(t, dialog.Window)
	u.fm.InjectEvents([]*vtinput.InputEvent{wizardKey(vtinput.VK_DOWN), wizardKey(vtinput.VK_RETURN)})
	if err := u.show(dialog); err != nil || list.SelectPos != 1 {
		t.Fatalf("arrow + Enter selected %d, error %v", list.SelectPos, err)
	}
}

func TestVTUIWizardOptionsPreserveDefaultsAndSpace(t *testing.T) {
	u := silentWizard(t)
	options := []wizardOption{
		{ID: "compact", Label: "Убрать точные повторы", Checked: true},
		{ID: "models", Label: "Скрыть названия моделей"},
		{ID: "images", Label: "Убрать картинки"},
		{ID: "summary", Label: "Добавить описания файлов"},
		{ID: "full", Label: "Добавить файлы целиком"},
		{ID: "base64", Label: "Другие вложения в base64"},
	}
	dialog, boxes := u.optionsDialog("Настройки экспорта", options)
	vtui.AssertLayout(t, dialog.Window)
	if boxes[0].State != 1 || boxes[1].State != 0 {
		t.Fatal("checkbox defaults were lost")
	}
	u.fm.InjectEvents([]*vtinput.InputEvent{wizardKey(vtinput.VK_DOWN), wizardKey(vtinput.VK_SPACE), wizardKey(vtinput.VK_RETURN)})
	if err := u.show(dialog); err != nil || boxes[0].State != 1 || boxes[1].State != 1 {
		t.Fatalf("Space should toggle focused checkbox only: %d, %d; %v", boxes[0].State, boxes[1].State, err)
	}
}

func TestVTUIWizardInputDefaultAndEscape(t *testing.T) {
	u := silentWizard(t)
	dialog, edit := u.inputDialog("Куда сохранить", "Новый файл для экспорта", "/tmp/handoff.zip")
	vtui.AssertLayout(t, dialog.Window)
	u.fm.InjectEvents([]*vtinput.InputEvent{wizardKey(vtinput.VK_RETURN)})
	if err := u.show(dialog); err != nil || edit.GetText() != "/tmp/handoff.zip" {
		t.Fatalf("Enter should accept destination default: %q, %v", edit.GetText(), err)
	}
	u.fm.InjectEvents([]*vtinput.InputEvent{wizardKey(vtinput.VK_ESCAPE)})
	if _, err := u.Select("Отмена", "", []wizardChoice{{Title: "Не выбирать"}}); !errors.Is(err, errWizardCanceled) {
		t.Fatalf("Escape error = %v", err)
	}
}

func TestVTUIWizardMessageAndHelpLayouts(t *testing.T) {
	u := silentWizard(t)
	dialog := u.messageDialog("Результат", strings.Repeat("Все данные сохранены. ", 80))
	vtui.AssertLayout(t, dialog.Window)
	u.fm.InjectEvents([]*vtinput.InputEvent{wizardKey(vtinput.VK_RETURN)})
	if err := u.show(dialog); err != nil {
		t.Fatal(err)
	}
	selection, _ := u.selectDialog("Сессии", "Полная справка по выбору", []wizardChoice{{Title: "Первая"}})
	u.fm.InjectEvents([]*vtinput.InputEvent{wizardKey(vtinput.VK_F1), wizardKey(vtinput.VK_ESCAPE), wizardKey(vtinput.VK_RETURN)})
	if err := u.show(selection); err != nil {
		t.Fatalf("closing help must not cancel the underlying selection: %v", err)
	}
}

func TestVTUIWizardOptionsFitSmallTerminal(t *testing.T) {
	u := silentWizard(t)
	u.fm.Screen().AllocBuf(60, 20)
	options := []wizardOption{{Label: "Сжать контекст"}, {Label: "Скрыть модели"}, {Label: "Убрать картинки"}, {Label: "Описания файлов"}, {Label: "Полное содержимое"}, {Label: "Прочие вложения в base64"}}
	dialog, _ := u.optionsDialog("Опции", options)
	vtui.AssertLayout(t, dialog.Window)
	if dialog.Y1 < 0 || dialog.Y2 >= 20 {
		t.Fatalf("dialog outside terminal: %d..%d", dialog.Y1, dialog.Y2)
	}
}

func TestVTUIWizardLabelsDoNotRenderTerminalControls(t *testing.T) {
	got := wizardVisibleText("hello\x1b[31m\n\tworld\x00")
	if strings.ContainsAny(got, "\x1b\n\t\x00") {
		t.Fatalf("unsafe label %q", got)
	}
}

func TestVTUIWizardBackButtons(t *testing.T) {
	for _, size := range [][2]int{{60, 20}, {80, 25}} {
		for _, locale := range []string{"en_US", "ru_RU"} {
			t.Run(locale, func(t *testing.T) {
				t.Setenv("LC_ALL", locale)
				u := silentWizard(t)
				u.fm.Screen().AllocBuf(size[0], size[1])
				u.SetBackEnabled(true)
				selection, _ := u.selectDialog("Choose", "Help", []wizardChoice{{Title: "First"}})
				options, _ := u.optionsDialog("Options", []wizardOption{{Label: "One"}, {Label: "Two"}, {Label: "Three"}, {Label: "Four"}, {Label: "Five"}, {Label: "Six"}})
				input, _ := u.inputDialog("Input", "Value", "default")
				for _, dialog := range []*wizardModal{selection, options, input} {
					vtui.AssertLayout(t, dialog.Window)
					var back *vtui.Button
					for _, child := range dialog.GetChildren() {
						if b, ok := child.(*vtui.Button); ok && strings.Contains(b.GetText(), uiText("Назад", "Back")) {
							back = b
						}
					}
					if back == nil {
						t.Fatal("Back button missing")
					}
					back.OnClick()
					if err := u.show(dialog); !errors.Is(err, errWizardBack) {
						t.Fatalf("Back returned %v", err)
					}
				}
				for _, dialog := range []*wizardModal{u.messageDialog("Message", "Done")} {
					for _, child := range dialog.GetChildren() {
						if b, ok := child.(*vtui.Button); ok && strings.Contains(b.GetText(), uiText("Назад", "Back")) {
							t.Fatal("Message has Back")
						}
					}
				}
				u.SetBackEnabled(false)
				first, _ := u.selectDialog("First", "", []wizardChoice{{Title: "First"}})
				for _, child := range first.GetChildren() {
					if b, ok := child.(*vtui.Button); ok && strings.Contains(b.GetText(), uiText("Назад", "Back")) {
						t.Fatal("First step has Back")
					}
				}
			})
		}
	}
}

func TestVTUIWizardBackKeyboardPreservesValues(t *testing.T) {
	u := silentWizard(t)
	u.SetBackEnabled(true)
	u.fm.InjectEvents([]*vtinput.InputEvent{wizardKey(vtinput.VK_SPACE), wizardKey(vtinput.VK_TAB), wizardKey(vtinput.VK_TAB), wizardKey(vtinput.VK_RETURN)})
	options, err := u.Options("Options", []wizardOption{{Label: "Toggle"}})
	if !errors.Is(err, errWizardBack) || len(options) != 1 || !options[0].Checked {
		t.Fatalf("Back options = %#v, %v", options, err)
	}
	u.fm.InjectEvents([]*vtinput.InputEvent{wizardKey(vtinput.VK_TAB), wizardKey(vtinput.VK_TAB), wizardKey(vtinput.VK_RETURN)})
	value, err := u.Input("Input", "Value", "saved value")
	if !errors.Is(err, errWizardBack) || value != "saved value" {
		t.Fatalf("Back input = %q, %v", value, err)
	}
	u.SetInitialSelection("second")
	dialog, list := u.selectDialog("Selection", "", []wizardChoice{{ID: "first", Title: "First"}, {ID: "second", Title: "Second"}})
	if list.SelectPos != 1 {
		t.Fatalf("restored selection = %d", list.SelectPos)
	}
	u.fm.InjectEvents([]*vtinput.InputEvent{{Type: vtinput.KeyEventType, KeyDown: true, VirtualKeyCode: 'C', ControlKeyState: vtinput.LeftCtrlPressed}})
	if err := u.show(dialog); !errors.Is(err, errWizardCanceled) {
		t.Fatalf("Ctrl+C = %v", err)
	}
}

func TestVTUIWizardBusyWaitsForCanceledRead(t *testing.T) {
	for _, key := range []*vtinput.InputEvent{wizardKey(vtinput.VK_ESCAPE), {Type: vtinput.KeyEventType, KeyDown: true, VirtualKeyCode: 'C', ControlKeyState: vtinput.LeftCtrlPressed}, wizardKey(vtinput.VK_RETURN)} {
		u := silentWizard(t)
		release := make(chan struct{})
		completed := false
		err := u.Busy("Read", "Reading local history…", func() error {
			u.fm.PostTask(func() {
				dialog := u.fm.GetTopFrame().(*wizardModal)
				vtui.AssertLayout(t, dialog.Window)
				if dialog.ShowClose {
					t.Error("busy modal has close icon")
				}
				cancel := dialog.onCancel
				dialog.onCancel = func() {
					cancel()
					if dialog.IsDone() {
						t.Error("cancel closed modal before worker completed")
					}
					foundFinishing := false
					for _, child := range dialog.GetChildren() {
						if label, ok := child.(*vtui.Text); ok && strings.Contains(label.GetText(), uiText("Завершаем текущее чтение", "Finishing current read")) {
							foundFinishing = true
						}
					}
					if !foundFinishing {
						t.Error("cancel did not show finishing-read status")
					}
					close(release)
				}
				for _, child := range dialog.GetChildren() {
					if button, ok := child.(*vtui.Button); ok {
						button.OnClick = dialog.onCancel
					}
				}
				u.fm.InjectEvents([]*vtinput.InputEvent{key})
			})
			<-release
			completed = true
			return nil
		})
		if !completed || !errors.Is(err, errWizardCanceled) {
			t.Fatalf("completed %v, error %v", completed, err)
		}
		if _, busy := u.fm.GetTopFrame().(*wizardModal); busy {
			t.Fatal("busy modal remained on stack")
		}
	}
}

func TestVTUIWizardBusyReturnsWorkResult(t *testing.T) {
	u := silentWizard(t)
	failure := errors.New("read failure")
	for _, want := range []error{nil, failure} {
		if got := u.Busy("Read", "Reading local history…", func() error { return want }); !errors.Is(got, want) {
			t.Fatalf("Busy = %v, want %v", got, want)
		}
		if _, busy := u.fm.GetTopFrame().(*wizardModal); busy {
			t.Fatal("busy modal remained on stack")
		}
	}
}
