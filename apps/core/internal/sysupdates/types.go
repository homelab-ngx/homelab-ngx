package sysupdates

// UpdateChange is one package that moved between two builds of a system.
type UpdateChange struct {
	From string           `json:"from" doc:"Empty = added."`
	Kind UpdateChangeKind `json:"kind" enum:"upgrade,downgrade,added,removed,changed"`
	Name string           `json:"name"`
	To   string           `json:"to" doc:"Empty = removed."`
}

// UpdateChangeKind is how a change moved.
type UpdateChangeKind string
