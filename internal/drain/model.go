// Package drain imports recorded agent context into a versioned portable format.
package drain

const Version = 1

type Bundle struct {
	Version        int               `json:"version"`
	Sessions       []Session         `json:"sessions"`
	Warnings       []string          `json:"warnings,omitempty"`
	Changes        []string          `json:"changes,omitempty"`
	Assets         map[string][]byte `json:"-"`
	AssetFiles     map[string]string `json:"asset_files,omitempty"`
	redactedModels []string
}

type Session struct {
	ID        string   `json:"id"`
	ParentID  string   `json:"parent_id,omitempty"`
	Source    string   `json:"source"`
	Title     string   `json:"title,omitempty"`
	UpdatedAt string   `json:"updated_at,omitempty"`
	CWD       string   `json:"cwd,omitempty"`
	Models    []string `json:"models,omitempty"`
	Events    []Event  `json:"events"`
	// Requests is the number of user requests counted cheaply while cataloging
	// when Events holds only a preview. Zero means unknown. Never serialized.
	Requests int `json:"-"`
}

type Event struct {
	ID     string  `json:"id,omitempty"`
	Time   string  `json:"time,omitempty"`
	Role   string  `json:"role"`
	Kind   string  `json:"kind,omitempty"`
	CallID string  `json:"call_id,omitempty"`
	Name   string  `json:"name,omitempty"`
	Blocks []Block `json:"blocks"`
}

type Block struct {
	Type        string `json:"type"`
	Text        string `json:"text,omitempty"`
	Path        string `json:"path,omitempty"`
	MIME        string `json:"mime,omitempty"`
	Asset       string `json:"asset,omitempty"`
	Description string `json:"description,omitempty"`
	Origin      string `json:"origin,omitempty"`
	CallID      string `json:"call_id,omitempty"`
	Name        string `json:"name,omitempty"`
	storedAsset string
}

type Options struct {
	Format           string
	StripModels      bool
	ModelNames       []string
	Files            string
	Roots            []string
	Compact          bool
	Images           string
	Descriptions     map[string]string
	Base64           bool
	PruneToolsBefore string
	RewriteCommand   string
	MaxFileBytes     int64
	DescribeCommand  string
	DescribeArgs     []string
	RewriteArgs      []string
	ImageArchive     string
}
