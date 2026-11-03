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

func TestVTUIWizardLabelsDoNotRenderTerminalControls(t *testing.T) {
	got := wizardVisibleText("hello\x1b[31m\n\tworld\x00")
	if strings.ContainsAny(got, "\x1b\n\t\x00") {
		t.Fatalf("unsafe label %q", got)
	}
}
