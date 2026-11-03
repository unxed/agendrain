package main

import (
	"encoding/json"
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
var errWizardBack = errors.New("wizard back")

func wizardEnvironments() []wizardChoice {
	var choices []wizardChoice
	for _, client := range []wizardChoice{
		{ID: "codex", Title: uiText("Codex (Desktop и консоль)", "Codex (Desktop and CLI)")},
		{ID: "claude", Title: uiText("Claude Code (консоль)", "Claude Code (CLI)")},
		{ID: "claude-desktop", Title: uiText("Claude Desktop (локальные Code/Cowork)", "Claude Desktop (local Code/Cowork)")},
		{ID: "opencode", Title: "OpenCode"},
		{ID: "cursor", Title: "Cursor (IDE)"},
		{ID: "cursor-agent", Title: uiText("Cursor Agent (консоль)", "Cursor Agent (CLI)")},
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

func wizardQuote(text string) string {
	return uiText("«", "“") + wizardUnquote(text) + uiText("»", "”")
}

// wizardUnquote makes a first request readable as a row label: a recorded
// slash command becomes "/name args", and one pair of quotes that the user
// typed around the whole request is dropped so the wizard's own quotes are not
// doubled. A shortened request keeps only its opening quote, which is dropped
// too.
func wizardUnquote(text string) string {
	text = strings.TrimSpace(text)
	if name, ok := wizardTag(text, "command-name"); ok && strings.HasPrefix(text, "<command-") {
		args, _ := wizardTag(text, "command-args")
		return strings.TrimSpace(name + " " + strings.Join(strings.Fields(args), " "))
	}
	for _, q := range [][2]string{{`"`, `"`}, {"«", "»"}, {"“", "”"}, {"'", "'"}} {
		if !strings.HasPrefix(text, q[0]) {
			continue
		}
		inner := strings.TrimPrefix(text, q[0])
		if strings.HasSuffix(inner, "…") && !strings.Contains(inner, q[1]) {
			return inner
		}
		inner = strings.TrimSuffix(inner, q[1])
		if len(inner)+len(q[0])+len(q[1]) == len(text) && !strings.Contains(inner, q[0]) && !strings.Contains(inner, q[1]) {
			return inner
		}
	}
	return text
}

func wizardTag(text, tag string) (string, bool) {
	start := strings.Index(text, "<"+tag+">")
	if start < 0 {
		return "", false
	}
	rest := text[start+len(tag)+2:]
	end := strings.Index(rest, "</"+tag+">")
	if end < 0 {
		return strings.TrimSpace(rest), true
	}
	return strings.TrimSpace(rest[:end]), true
}

// wizardRequests spells the request count in plain words with correct
// Russian plural forms.
func wizardRequests(n int) string {
	if !russianLocale(os.Getenv, nativeRussianLocale) {
		if n == 1 {
			return "1 request"
		}
		return fmt.Sprintf("%d requests", n)
	}
	word := "запросов"
	if n%100 < 11 || n%100 > 14 {
		switch n % 10 {
		case 1:
			word = "запрос"
		case 2, 3, 4:
			word = "запроса"
		}
	}
	return fmt.Sprintf("%d %s", n, word)
}

// wizardTilde abbreviates a path under the home directory as ~/....
func wizardTilde(cwd, home string) string {
	label := wizardProjectLabel(cwd, home)
	if label == "~" {
		return "~"
	}
	h := strings.TrimRight(strings.ReplaceAll(home, "\\", "/"), "/")
	c := strings.ReplaceAll(cwd, "\\", "/")
	if h != "" && len(c) > len(h)+1 && c[len(h)] == '/' && strings.EqualFold(c[:len(h)], h) {
		return "~" + c[len(h):]
	}
	return cwd
}

func wizardHome() string {
	home, _ := os.UserHomeDir()
	return home
}

// wizardProjectLabel is the short project tag of a row: "~" for the home
// directory itself, "~/rel" for short or numeric names below it (ambiguous
// as bare basenames), otherwise the basename.
func wizardProjectLabel(cwd, home string) string {
	norm := func(p string) string {
		return strings.TrimRight(strings.ReplaceAll(p, "\\", "/"), "/")
	}
	c, h := norm(cwd), norm(home)
	if c == "" {
		return ""
	}
	if h != "" {
		fold := len(h) > 1 && h[1] == ':'
		eq := func(a, b string) bool {
			if fold {
				return strings.EqualFold(a, b)
			}
			return a == b
		}
		if eq(c, h) {
			return "~"
		}
		if len(c) > len(h)+1 && c[len(h)] == '/' && eq(c[:len(h)], h) {
			rel := c[len(h)+1:]
			name := path.Base(rel)
			ambiguous := len([]rune(name)) <= 2
			if !ambiguous {
				ambiguous = strings.Trim(name, "0123456789") == ""
			}
			if ambiguous {
				return "~/" + rel
			}
		}
	}
	return wizardProject(cwd)
}

// wizardRequestCount returns the number of user requests when it is already
// known without extra loading, or 0 when unknown.
func wizardRequestCount(s drain.Session) int {
	if s.Requests > 0 {
		return s.Requests
	}
	n := 0
	for _, event := range s.Events {
		if event.Kind == "preview" {
			return 0
		}
		if event.Role != "user" {
			continue
		}
		for _, block := range event.Blocks {
			if strings.TrimSpace(wizardBlockText(block)) != "" || (block.Type == "image" || block.Type == "file") && (block.Asset != "" || block.Path != "") {
				n++
				break
			}
		}
	}
	return n
}

func wizardSessionTime(s drain.Session) time.Time {
	var latest time.Time
	if when, err := time.Parse(time.RFC3339Nano, s.UpdatedAt); err == nil {
		latest = when
	}
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
		if session.ParentID == "" && (session.Source == "" || session.Source == "codex" || wizardHasConversation(session)) {
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
				if text := wizardBlockText(block); strings.TrimSpace(text) != "" {
					snippet = wizardShort(text, 400)
					break
				}
			}
			if snippet != "" {
				break
			}
		}
		project0 := wizardProject(session.CWD)
		title := session.Title
		if title == project0 {
			title = ""
		}
		untitled := uiText("Без названия", "Untitled")
		if title == "" {
			title = untitled
			if snippet != "" {
				prefix := untitled + ": "
				title = prefix + wizardQuote(wizardShort(wizardUnquote(snippet), 160))
			}
		} else {
			title = wizardShort(title, 160)
		}
		var detail []string
		if session.Title != "" && session.Title != project0 {
			detail = append(detail, uiText("Название: ", "Title: ")+session.Title)
		} else {
			detail = append(detail, uiText("Название: нет", "Title: none"))
		}
		if snippet != "" {
			detail = append(detail, uiText("Первый запрос: ", "First request: ")+wizardQuote(wizardShort(wizardUnquote(snippet), 400)))
		}
		if session.CWD != "" {
			detail = append(detail, uiText("Папка: ", "Folder: ")+wizardTilde(session.CWD, wizardHome()))
		}
		var tail []string
		if when := wizardSessionTime(session); !when.IsZero() {
			tail = append(tail, uiText("Последняя активность: ", "Last activity: ")+when.UTC().Format("2006-01-02 15:04"))
		}
		count := wizardRequestCount(session)
		if count > 0 {
			tail = append(tail, uiText("Запросов: ", "Requests: ")+fmt.Sprint(count))
		}
		tail = append(tail, "ID: "+session.ID)
		detail = append(detail, strings.Join(tail, " · "))
		when := uiText("дата ?", "date ?")
		if t := wizardSessionTime(session); !t.IsZero() {
			when = t.UTC().Format("2006-01-02 15:04")
		}
		project := wizardProjectLabel(session.CWD, wizardHome())
		if strings.Contains(strings.ToLower(strings.ReplaceAll(session.CWD, "\\", "/")), "/claude/scratch-workspaces/") {
			project = "Desktop"
		}
		if project == "" {
			project = uiText("без проекта", "no project")
		}
		row := when
		if count > 0 {
			row += " · " + wizardRequests(count)
		}
		row += " · [" + wizardShort(project, 32) + "] " + title
		choices = append(choices, wizardChoice{ID: session.ID, Title: row, Detail: strings.Join(detail, "\n")})
	}
	counts := map[string]int{}
	for _, choice := range choices {
		counts[choice.Title]++
	}
	for i := range choices {
		if counts[choices[i].Title] > 1 {
			choices[i].Title = wizardShort(choices[i].ID, 8) + fmt.Sprintf(" #%d · ", i+1) + choices[i].Title
		}
	}
	return choices
}

func wizardBlockText(block drain.Block) string {
	if block.Type == "image" {
		return uiText("[изображение]", "[image]")
	}
	if block.Type == "file" {
		return uiText("Файл: ", "File: ") + wizardProject(block.Path)
	}
	if block.Type == "text" {
		return block.Text
	}
	if block.Type != "context" {
		return ""
	}
	var v any
	if json.Unmarshal([]byte(block.Text), &v) != nil {
		return ""
	}
	var extract func(any) string
	extract = func(v any) string {
		switch x := v.(type) {
		case string:
			return x
		case []any:
			for _, item := range x {
				if s := extract(item); s != "" {
					return s
				}
			}
		case map[string]any:
			for _, key := range []string{"text", "content", "message"} {
				if s := extract(x[key]); s != "" {
					return s
				}
			}
		}
		return ""
	}
	return extract(v)
}

func wizardHasConversation(session drain.Session) bool {
	for _, event := range session.Events {
		if event.Role != "user" && event.Role != "assistant" {
			continue
		}
		for _, block := range event.Blocks {
			if strings.TrimSpace(wizardBlockText(block)) != "" {
				return true
			}
			if (block.Type == "image" || block.Type == "file") && (block.Asset != "" || block.Path != "") {
				return true
			}
			if (block.Type == "reasoning" || block.Type == "tool_call") && strings.TrimSpace(block.Text) != "" {
				return true
			}
		}
	}
	return false
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

func wizardBackEnabled(ui wizardUI, enabled bool) {
	if nav, ok := ui.(interface{ SetBackEnabled(bool) }); ok {
		nav.SetBackEnabled(enabled)
	}
}

func wizardRemember(ui wizardUI, id string) {
	if nav, ok := ui.(interface{ SetInitialSelection(string) }); ok {
		nav.SetInitialSelection(id)
	}
}

func wizardWork(ui wizardUI, title, text string, work func() error) error {
	if busy, ok := ui.(interface {
		Busy(string, string, func() error) error
	}); ok {
		return busy.Busy(title, text, work)
	}
	return work()
}

func wizardOptions(format string, checked map[string]bool) []wizardOption {
	options := []wizardOption{
		{ID: "compact", Label: uiText("Сжать контекст: убрать точные повторы", "Compact context: remove exact duplicates"), Detail: uiText("Формулировки требований и решений сохраняются, повторы заменяются ссылками", "Keep original requirements and decisions; replace exact duplicates with references")},
		{ID: "strip-models", Label: uiText("Скрыть названия моделей", "Remove model names"), Detail: uiText("Удалить имена моделей из текста и метаданных", "Remove model names from text and metadata")},
		{ID: "images-omit", Label: uiText("Убрать картинки", "Omit images"), Detail: uiText("Вместо картинок останутся отметки об их исключении", "Replace images with omission markers")},
		{ID: "files-summary", Label: uiText("Добавить краткие описания файлов", "Include brief file descriptions"), Detail: uiText("Только файлы внутри выбранной вами разрешённой папки", "Only files inside a directory you explicitly allow")},
		{ID: "files-full", Label: uiText("Добавить содержимое файлов целиком", "Include full file contents"), Detail: uiText("Только файлы внутри выбранной вами разрешённой папки", "Only files inside a directory you explicitly allow")},
	}
	if format != "zip" {
		options = append(options, wizardOption{ID: "base64", Label: uiText("Добавить прочие вложения в base64", "Include other attachments as base64"), Detail: uiText("Для вложений, которые не являются текстом или картинками; увеличивает размер", "Include other binary attachments; increases output size")})
	}
	for i := range options {
		options[i].Checked = checked[options[i].ID]
	}
	return options
}

func runWizard(ui wizardUI, out io.Writer) error {
	environments := wizardEnvironments()
	if len(environments) == 0 {
		text := uiText("Локальная история агентов не найдена. Для скопированной истории: agendrain export --source CLIENT --input PATH --output handoff.zip.", "No local agent stores were found. For a copied store, use agendrain export --source CLIENT --input PATH --output handoff.zip.")
		if err := ui.Message(uiText("Локальная история не найдена", "No local history found"), text); err != nil {
			return err
		}
		return errors.New(text)
	}
	var environment, selected, format wizardChoice
	var catalog *drain.Bundle
	var session drain.Session
	checked := map[string]bool{}
	var allowedRoot, destination string
	stage := 0
	for {
		wizardBackEnabled(ui, stage > 0)
		switch stage {
		case 0:
			wizardRemember(ui, environment.ID)
			choice, err := wizardSelect(ui, uiText("Откуда перенести диалог?", "Choose an environment"), uiText("Показаны среды с найденной локальной историей.", "Environments with local history are listed."), environments)
			if errors.Is(err, errWizardBack) {
				return errWizardCanceled
			}
			if err != nil {
				return err
			}
			if choice.ID != environment.ID {
				catalog = nil
				selected = wizardChoice{}
				allowedRoot = ""
				destination = ""
			}
			environment = choice
			stage = 1
		case 1:
			if catalog == nil {
				err := wizardWork(ui, environment.Title, uiText("Читаю список диалогов…", "Reading conversations…"), func() error {
					var err error
					if environment.ID == "codex" {
						catalog, err = drain.CatalogCodex("")
					} else {
						catalog, err = loadCatalogInput(environment.ID)
					}
					return err
				})
				if errors.Is(err, errWizardCanceled) {
					return err
				}
				if err != nil {
					if e := ui.Message(uiText("Не удалось прочитать историю", "Could not read history"), fmt.Sprintf("%s: %v", environment.Title, err)); e != nil && !errors.Is(e, errWizardBack) {
						return e
					}
					stage = 0
					continue
				}
			}
			choices := wizardSessionChoices(catalog)
			if len(choices) == 0 {
				if err := ui.Message(uiText("Нет доступных диалогов", "No conversations available"), fmt.Sprintf(uiText("В %s не найдены диалоги с доступными сообщениями. Пустые записи и субагенты отдельно не показываются.", "No conversations with available messages were found in %s. Empty records and subagents are not listed separately."), environment.Title)); err != nil && !errors.Is(err, errWizardBack) {
					return err
				}
				stage = 0
				continue
			}
			wizardRemember(ui, selected.ID)
			choice, err := wizardSelect(ui, fmt.Sprintf(uiText("Сессии · %s", "Sessions · %s"), environment.Title), uiText("Каждая строка — один диалог: дата, запросы, [папка], название", "Each line is one conversation: date, requests, [folder], title"), choices)
			if errors.Is(err, errWizardBack) {
				stage = 0
				continue
			}
			if err != nil {
				return err
			}
			if selected.ID != choice.ID {
				allowedRoot = ""
				destination = ""
			}
			selected = choice
			for _, candidate := range catalog.Sessions {
				if candidate.ID == selected.ID {
					session = candidate
					break
				}
			}
			stage = 2
		case 2:
			wizardRemember(ui, format.ID)
			choice, err := wizardSelect(ui, uiText("Формат сохранения", "Output format"), uiText("По умолчанию — один ZIP со всей доступной историей и вложениями.", "Default: one ZIP containing all available history and attachments."), []wizardChoice{
				{ID: "zip", Title: uiText("ZIP — один файл (рекомендуется)", "ZIP — one file (recommended)"), Detail: uiText("Основной диалог, отдельные истории субагентов, метаданные и вложения", "Main conversation, linked subagent histories, metadata and attachments")},
				{ID: "md-images", Title: uiText("Markdown + папка картинок", "Markdown + image folder"), Detail: uiText("Один длинный документ и пронумерованные картинки для AI Studio", "One long document and numbered images for AI Studio")},
				{ID: "md-image-zip", Title: uiText("Markdown + архив картинок", "Markdown + image ZIP"), Detail: uiText("Два файла: длинный Markdown и ZIP с пронумерованными картинками", "Two files: a long Markdown document and a ZIP of numbered images")},
			})
			if errors.Is(err, errWizardBack) {
				stage = 1
				continue
			}
			if err != nil {
				return err
			}
			if choice.ID != format.ID {
				destination = ""
			}
			format = choice
			stage = 3
		case 3:
			options, err := ui.Options(uiText("Дополнительные опции", "Export options"), wizardOptions(format.ID, checked))
			for _, option := range options {
				checked[option.ID] = option.Checked
			}
			if errors.Is(err, errWizardBack) {
				stage = 2
				continue
			}
			if err != nil {
				return err
			}
			if checked["files-summary"] && checked["files-full"] {
				if err := ui.Message(uiText("Выберите один режим файлов", "Choose one file mode"), uiText("Выберите краткие описания или полное содержимое файлов, но не оба режима сразу.", "Choose brief descriptions or full file contents, rather than both.")); err != nil && !errors.Is(err, errWizardBack) {
					return err
				}
				continue
			}
			if checked["files-summary"] || checked["files-full"] {
				stage = 4
			} else {
				stage = 5
			}
		case 4:
			initial := allowedRoot
			if initial == "" {
				initial = session.CWD
			}
			value, err := ui.Input(uiText("Какие файлы можно прочитать?", "Which files may be read?"), uiText("Разрешённая папка", "Allowed directory"), initial)
			allowedRoot = value
			if errors.Is(err, errWizardBack) {
				stage = 3
				continue
			}
			if err != nil {
				return err
			}
			info, e := os.Stat(allowedRoot)
			if strings.TrimSpace(allowedRoot) == "" || e != nil || !info.IsDir() {
				if err := ui.Message(uiText("Нужна существующая папка", "Existing directory required"), uiText("Укажите существующую папку, внутри которой разрешено читать файлы.", "Choose an existing directory in which files may be read.")); err != nil && !errors.Is(err, errWizardBack) {
					return err
				}
				continue
			}
			stage = 5
		case 5:
			if destination == "" {
				destination = wizardDefaultDestination(session, format.ID)
			}
			value, err := ui.Input(uiText("Куда сохранить диалог?", "Save conversation"), uiText("Новый файл для сохранения", "New output file"), destination)
			destination = value
			if errors.Is(err, errWizardBack) {
				if checked["files-summary"] || checked["files-full"] {
					stage = 4
				} else {
					stage = 3
				}
				continue
			}
			if err != nil {
				return err
			}
			if strings.TrimSpace(destination) == "" {
				return errors.New(uiText("Укажите файл для сохранения", "Destination path is required"))
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
				o.Roots = []string{allowedRoot}
			}
			if format.ID == "md-image-zip" {
				o.ImageArchive = strings.TrimSuffix(destination, filepath.Ext(destination)) + "_images.zip"
			}
			var b *drain.Bundle
			err = wizardWork(ui, environment.Title, uiText("Готовлю выбранный диалог и его субагентов…", "Preparing the selected conversation and its subagents…"), func() error {
				var err error
				b, err = loadInput(environment.ID, "", selected.ID)
				if err != nil {
					return err
				}
				if err = b.Select(selected.ID); err != nil {
					return err
				}
				return drain.Prepare(b, o)
			})
			if err != nil {
				return err
			}
			if len(b.Warnings) > 0 {
				if err := ui.Message(uiText("Ограничения исходной истории", "Source history limitations"), strings.Join(b.Warnings, "\n")); err != nil {
					if errors.Is(err, errWizardBack) {
						continue
					}
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
			entry := "conversation.md"
			if o.Format == "md" {
				entry = filepath.Base(destination)
			}
			message := fmt.Sprintf(uiText("Диалог сохранён:\n\n%s\n\nПередайте результат следующему агенту и попросите прочитать %s.", "Conversation saved:\n\n%s\n\nGive the result to the next agent and ask it to read %s."), fullPath, entry)
			if o.ImageArchive != "" {
				message += uiText("\n\nАрхив картинок: ", "\n\nImage archive: ") + o.ImageArchive
			} else if o.Format == "md" {
				images := strings.TrimSuffix(fullPath, filepath.Ext(fullPath)) + "_images"
				if info, e := os.Stat(images); e == nil && info.IsDir() {
					message += uiText("\n\nТакже передайте папку картинок:\n", "\n\nAlso include the image folder:\n") + images
				}
			}
			wizardBackEnabled(ui, false)
			_ = ui.Message(uiText("Готово", "Done"), message)
			fmt.Fprintf(out, "Saved %s (%d sessions)\n", destination, len(b.Sessions))
			return nil
		}
	}
}
