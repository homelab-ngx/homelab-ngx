package secrets

import "time"

// SecretStatus is the metadata of one secret; never its value.
type SecretStatus struct {
	Description string     `json:"description"`
	ModifiedAt  *time.Time `json:"modified_at" doc:"Null if unknown."`
	Name        string     `json:"name" doc:"service/KEY."`
	Present     bool       `json:"present"`
	Preview     string     `json:"preview,omitempty" doc:"First 3 characters followed by \"•••\"."`
	RunPath     string     `json:"run_path" doc:"Runtime path, e.g. /run/secrets/caddy/CLOUDFLARE_API_TOKEN."`
	Services    []string   `json:"services"`
}

// SecretsList is every registered secret and whether a deploy is pending.
type SecretsList struct {
	DeployPending bool           `json:"deploy_pending"`
	Secrets       []SecretStatus `json:"secrets"`
}

// SecretSet sets or rotates a secret.
type SecretSet struct {
	Description string `json:"description,omitempty" doc:"Accepted and ignored."`
	Value       string `json:"value" minLength:"1"`
}

// SecretMutation is the result of a committed secret change.
type SecretMutation struct {
	Branch        string   `json:"branch"`
	Commit        string   `json:"commit"`
	DeployPending bool     `json:"deploy_pending"`
	PushPending   bool     `json:"push_pending" doc:"Committed locally but the push failed."`
	Warnings      []string `json:"warnings"`
}
