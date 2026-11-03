package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/unxed/agendrain/internal/drain"
)

type wizardChoice struct{ ID, Title, Detail string }
type wizardOption struct {
	ID, Label, Detail string
	Checked           bool
}

type wizardUI interface {
	Select(title, help string, choices []wizardChoice) (int, error)
	Options(title string, options []wizardOption) ([]wizardOption, error)
	Input(title, label, initial string) (string, error)
	Message(title, text string) error
}

var errWizardCanceled = errors.New("wizard canceled")

func wizardEnvironments() []wizardChoice {
	var choices []wizardChoice
	for _, client := range []wizardChoice{
		{ID: "codex", Title: "Codex (Desktop и консоль)"},
		{ID: "claude", Title: "Claude Code (консоль)"},
		{ID: "claude-desktop", Title: "Claude Desktop (локальные Code/Cowork)"},
		{ID: "opencode", Title: "OpenCode"},
		{ID: "cursor", Title: "Cursor"},
		{ID: "vtvibe", Title: "vtvibe (f4)"},
	} {
		candidates := drain.Discover(client.ID)
		// Claude Code's shared projects directory alone does not establish
		// that a separate Desktop installation has local sessions.
		if client.ID == "claude-desktop" && len(candidates) > 1 {
			candidates = candidates[:1]
		}
		var existing []string
		for _, candidate := range candidates {
			if _, err := os.Stat(candidate); err == nil {
				existing = append(existing, candidate)
			}
		}
		if len(existing) > 0 {
			client.Detail = strings.Join(existing, " · ")
			choices = append(choices, client)
		}
	}
	return choices
}

func wizardShort(text string, limit int) string {
	text = strings.Join(strings.Fields(text), " ")
	runes := []rune(text)
	if len(runes) > limit {
		return string(runes[:limit-1]) + "…"
	}
	return text
}

func wizardProject(cwd string) string {
	if cwd == "" {
		return ""
	}
	name := path.Base(strings.ReplaceAll(cwd, "\\", "/"))
	if name == "." || name == "/" {
		return ""
	}
	return name
}

func wizardSessionTime(s drain.Session) time.Time {
	var latest time.Time
	for _, event := range s.Events {
		if when, err := time.Parse(time.RFC3339Nano, event.Time); err == nil && when.After(latest) {
			latest = when
		}
	}
	return latest
}

func wizardSessionChoices(b *drain.Bundle) []wizardChoice {
	var sessions []drain.Session
	for _, session := range b.Sessions {
		if session.ParentID == "" {
			sessions = append(sessions, session)
		}
	}
	sort.SliceStable(sessions, func(i, j int) bool {
		a, z := wizardSessionTime(sessions[i]), wizardSessionTime(sessions[j])
		if !a.Equal(z) {
			return a.After(z)
		}
		return sessions[i].ID > sessions[j].ID
	})
	choices := make([]wizardChoice, 0, len(sessions))
	for _, session := range sessions {
		var snippet string
		for _, event := range session.Events {
			if event.Role != "user" {
				continue
			}
			for _, block := range event.Blocks {
				if block.Type == "text" && strings.TrimSpace(block.Text) != "" {
					snippet = wizardShort(block.Text, 120)
					break
				}
			}
			if snippet != "" {
				break
			}
		}
		title := session.Title
		if title == wizardProject(session.CWD) && snippet != "" {
			title += " — " + snippet
		}
		if title == "" {
			title = snippet
		}
		if title == "" {
			title = wizardProject(session.CWD)
		}
		if title == "" {
			title = "Диалог"
		}
		var detail []string
		if session.CWD != "" {
			detail = append(detail, session.CWD)
		}
		if when := wizardSessionTime(session); !when.IsZero() {
			detail = append(detail, when.UTC().Format("2006-01-02 15:04 UTC"))
		}
		if snippet != "" && snippet != title {
			detail = append(detail, snippet)
		}
		detail = append(detail, "ID: "+session.ID)
		choices = append(choices, wizardChoice{ID: session.ID, Title: wizardShort(title, 140), Detail: strings.Join(detail, " · ")})
	}
	return choices
}

func wizardSelect(ui wizardUI, title, help string, choices []wizardChoice) (wizardChoice, error) {
	index, err := ui.Select(title, help, choices)
	if err != nil {
		return wizardChoice{}, err
	}
	if index < 0 || index >= len(choices) {
		return wizardChoice{}, fmt.Errorf("invalid selection for %s", title)
	}
	return choices[index], nil
}

func wizardDefaultDestination(session drain.Session, format string) string {
	name := wizardProject(session.CWD)
	if name == "" {
		name = session.Title
	}
	if name == "" {
		name = "conversation"
	}
	var safe strings.Builder
	for _, char := range name {
		if unicode.IsLetter(char) || unicode.IsDigit(char) || char == '-' || char == '_' {
			safe.WriteRune(char)
		} else {
			safe.WriteByte('-')
		}
	}
	prefix := strings.Trim(wizardShort(safe.String(), 60), "-. ")
	if prefix == "" {
		prefix = "conversation"
	}
	extension := ".zip"
	if format != "zip" {
		extension = ".md"
	}
	return prefix + "-" + time.Now().UTC().Format("20060102T150405Z") + extension
}

func runWizard(ui wizardUI, out io.Writer) error {
	environments := wizardEnvironments()
	if len(environments) == 0 {
		text := "No local agent stores were found. For a copied store, use agendrain export --source CLIENT --input PATH --output handoff.zip."
		if err := ui.Message("Локальная история не найдена", text); err != nil {
			return err
		}
		return errors.New(text)
	}
	environment, err := wizardSelect(ui, "Откуда перенести диалог?", "Показаны среды с найденной локальной историей.", environments)
	if err != nil {
		return err
	}
	var catalog *drain.Bundle
	if environment.ID == "codex" {
		catalog, err = drain.CatalogCodex("")
	} else {
		catalog, err = loadInput(environment.ID, "", "")
	}
	if err != nil {
		return fmt.Errorf("cannot list local %s conversations: %w", environment.Title, err)
	}
	choices := wizardSessionChoices(catalog)
	if len(choices) == 0 {
		return fmt.Errorf("no root conversations found in %s; inspect a copied store with --input PATH", environment.Title)
	}
	selected, err := wizardSelect(ui, "Выберите сессию", "Сначала недавние сессии. Истории субагентов включаются автоматически.", choices)
	if err != nil {
		return err
	}
	format, err := wizardSelect(ui, "Формат сохранения", "По умолчанию — один ZIP со всей доступной историей и вложениями.", []wizardChoice{
		{ID: "zip", Title: "ZIP — один файл (рекомендуется)", Detail: "Основной диалог, отдельные истории субагентов, метаданные и вложения"},
		{ID: "md-images", Title: "Markdown + папка картинок", Detail: "Один длинный документ и пронумерованные картинки для AI Studio"},
		{ID: "md-image-zip", Title: "Markdown + архив картинок", Detail: "Два файла: длинный Markdown и ZIP с пронумерованными картинками"},
	})
	if err != nil {
		return err
	}
	options := []wizardOption{
		{ID: "compact", Label: "Сжать контекст: убрать точные повторы", Detail: "Формулировки требований и решений сохраняются, повторы заменяются ссылками"},
		{ID: "strip-models", Label: "Скрыть названия моделей", Detail: "Удалить имена моделей из текста и метаданных"},
		{ID: "images-omit", Label: "Убрать картинки", Detail: "Вместо картинок останутся отметки об их исключении"},
		{ID: "files-summary", Label: "Добавить краткие описания файлов", Detail: "Только файлы внутри выбранной вами разрешённой папки"},
		{ID: "files-full", Label: "Добавить содержимое файлов целиком", Detail: "Только файлы внутри выбранной вами разрешённой папки"},
	}
	if format.ID != "zip" {
		options = append(options, wizardOption{ID: "base64", Label: "Добавить прочие вложения в base64", Detail: "Для вложений, которые не являются текстом или картинками; увеличивает размер"})
	}
	var checked map[string]bool
	for {
		options, err = ui.Options("Дополнительные опции", options)
		if err != nil {
			return err
		}
		checked = map[string]bool{}
		for _, option := range options {
			checked[option.ID] = option.Checked
		}
		if !checked["files-summary"] || !checked["files-full"] {
			break
		}
		if err := ui.Message("Выберите один режим файлов", "Выберите краткие описания или полное содержимое файлов, но не оба режима сразу."); err != nil {
			return err
		}
	}
	var session drain.Session
	for _, candidate := range catalog.Sessions {
		if candidate.ID == selected.ID {
			session = candidate
			break
		}
	}
	o := drain.Options{Format: "zip", Files: "none", Images: "keep", Compact: checked["compact"], StripModels: checked["strip-models"], Base64: format.ID != "zip" && checked["base64"]}
	if format.ID != "zip" {
		o.Format = "md"
	}
	if checked["images-omit"] {
		o.Images = "omit"
	}
	if checked["files-summary"] || checked["files-full"] {
		o.Files = "summary"
		if checked["files-full"] {
			o.Files = "full"
		}
		root, err := ui.Input("Какие файлы можно прочитать?", "Разрешённая папка", session.CWD)
		if err != nil {
			return err
		}
		if strings.TrimSpace(root) == "" {
			return fmt.Errorf("local file inclusion requires an explicit allowed directory")
		}
		info, err := os.Stat(root)
		if err != nil || !info.IsDir() {
			return fmt.Errorf("allowed file root must be an existing directory: %s", root)
		}
		o.Roots = []string{root}
	}
	destination, err := ui.Input("Куда сохранить диалог?", "Новый файл для сохранения", wizardDefaultDestination(session, format.ID))
	if err != nil {
		return err
	}
	if strings.TrimSpace(destination) == "" {
		return fmt.Errorf("destination path is required")
	}
	if format.ID == "md-image-zip" {
		o.ImageArchive = strings.TrimSuffix(destination, filepath.Ext(destination)) + "_images.zip"
	}
	b, err := loadInput(environment.ID, "", selected.ID)
	if err != nil {
		return err
	}
	if err := b.Select(selected.ID); err != nil {
		return err
	}
	if err := drain.Prepare(b, o); err != nil {
		return err
	}
	if len(b.Warnings) > 0 {
		if err := ui.Message("Ограничения исходной истории", strings.Join(b.Warnings, "\n")); err != nil {
			return err
		}
	}
	if err := drain.Write(b, destination, o); err != nil {
		return err
	}
	fullPath, err := filepath.Abs(destination)
	if err != nil {
		fullPath = destination
	}
	message := "Диалог сохранён:\n\n" + fullPath + "\n\nПередайте результат следующему агенту и попросите прочитать conversation.md."
	if o.ImageArchive != "" {
		message += "\n\nАрхив картинок: " + o.ImageArchive
	}
	_ = ui.Message("Готово", message)
	fmt.Fprintf(out, "Saved %s (%d sessions)\n", destination, len(b.Sessions))
	return nil
}
