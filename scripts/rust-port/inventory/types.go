package inventory

import "encoding/json"

type Oracle struct {
	Commit  string `json:"commit"`
	Release string `json:"release"`
}

type Group struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

type ArgProbe struct {
	Count    int    `json:"count"`
	Accepted bool   `json:"accepted"`
	Error    string `json:"error,omitempty"`
}

type FlagSpec struct {
	Name                string              `json:"name"`
	Shorthand           string              `json:"shorthand,omitempty"`
	Usage               string              `json:"usage"`
	Type                string              `json:"type"`
	Default             string              `json:"default"`
	NoOptDefault        string              `json:"no_opt_default,omitempty"`
	Hidden              bool                `json:"hidden"`
	Deprecated          string              `json:"deprecated,omitempty"`
	ShorthandDeprecated string              `json:"shorthand_deprecated,omitempty"`
	Annotations         map[string][]string `json:"annotations,omitempty"`
}

type CommandSpec struct {
	Path                string            `json:"path"`
	Name                string            `json:"name"`
	Use                 string            `json:"use"`
	Short               string            `json:"short,omitempty"`
	Long                string            `json:"long,omitempty"`
	Example             string            `json:"example,omitempty"`
	Aliases             []string          `json:"aliases,omitempty"`
	SuggestFor          []string          `json:"suggest_for,omitempty"`
	ValidArgs           []string          `json:"valid_args,omitempty"`
	ArgAliases          []string          `json:"arg_aliases,omitempty"`
	Hidden              bool              `json:"hidden"`
	Deprecated          string            `json:"deprecated,omitempty"`
	GroupID             string            `json:"group_id,omitempty"`
	Annotations         map[string]string `json:"annotations,omitempty"`
	DisableFlagParsing  bool              `json:"disable_flag_parsing"`
	TraverseChildren    bool              `json:"traverse_children"`
	Runnable            bool              `json:"runnable"`
	HasSubcommands      bool              `json:"has_subcommands"`
	ArgumentCountProbes []ArgProbe        `json:"argument_count_probes,omitempty"`
	LocalFlags          []FlagSpec        `json:"local_flags,omitempty"`
	PersistentFlags     []FlagSpec        `json:"persistent_flags,omitempty"`
}

type CobraTreeDocument struct {
	SchemaVersion int           `json:"schema_version"`
	Oracle        Oracle        `json:"oracle"`
	Groups        []Group       `json:"groups"`
	Commands      []CommandSpec `json:"commands"`
}

type SymRoomFlag struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Default string `json:"default,omitempty"`
	Usage   string `json:"usage"`
}

type SymRoomAction struct {
	Name         string        `json:"name"`
	UsageStrings []string      `json:"usage_strings,omitempty"`
	Flags        []SymRoomFlag `json:"flags,omitempty"`
}

type SymRoomSubcommand struct {
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	Handler      string          `json:"handler"`
	Source       string          `json:"source"`
	UsageStrings []string        `json:"usage_strings,omitempty"`
	Actions      []SymRoomAction `json:"actions,omitempty"`
	Flags        []SymRoomFlag   `json:"flags,omitempty"`
}

type SymRoomGrammarDocument struct {
	SchemaVersion int                 `json:"schema_version"`
	Oracle        Oracle              `json:"oracle"`
	UsageText     string              `json:"usage_text"`
	Subcommands   []SymRoomSubcommand `json:"subcommands"`
}

type MCPToolSpec struct {
	Name             string          `json:"name"`
	Order            int             `json:"order"`
	Description      string          `json:"description"`
	InputSchema      json.RawMessage `json:"input_schema"`
	ReadOnly         bool            `json:"read_only"`
	Destructive      bool            `json:"destructive"`
	IsAlias          bool            `json:"is_alias,omitempty"`
	ApprovalGranting bool            `json:"approval_granting"`
}

type MCPToolDocument struct {
	SchemaVersion int           `json:"schema_version"`
	Oracle        Oracle        `json:"oracle"`
	ServerName    string        `json:"server_name"`
	ServerVersion string        `json:"server_version"`
	Instructions  string        `json:"instructions,omitempty"`
	Tools         []MCPToolSpec `json:"tools"`
}

type HTTPRouteSpec struct {
	Method  string `json:"method"`
	Path    string `json:"path"`
	Auth    string `json:"auth"` // registration-level: "none" or "auth_middleware"
	Handler string `json:"handler"`
}

type HTTPRouteDocument struct {
	SchemaVersion int             `json:"schema_version"`
	Oracle        Oracle          `json:"oracle"`
	Routes        []HTTPRouteSpec `json:"routes"`
}

type SurfaceCounts struct {
	SymdeskTotalCommands   int `json:"symdesk_total_commands"`
	SymdeskNonRootCommands int `json:"symdesk_non_root_commands"`
	SymroomSubcommands     int `json:"symroom_subcommands"`
	SymdeskMCPTools        int `json:"symdesk_mcp_tools"`
	SymroomMCPTools        int `json:"symroom_mcp_tools"`
	SelfhostHTTPRoutes     int `json:"selfhost_http_routes"`
}

type ProvenanceDocument struct {
	SchemaVersion          int               `json:"schema_version"`
	Oracle                 Oracle            `json:"oracle"`
	ProductionSourceDigest string            `json:"production_source_digest"`
	GeneratorSourceDigest  string            `json:"generator_source_digest"`
	SurfaceCounts          SurfaceCounts     `json:"surface_counts"`
	FixtureChecksums       map[string]string `json:"fixture_checksums"`
}
