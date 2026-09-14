package graphsnapshot

// ActiveSnapshot identifies the namespace head and its immutable source in
// one read. Both pointer fields are null when no head has been published.
type ActiveSnapshot struct {
	Namespace     string  `json:"namespace"`
	ActiveVersion *string `json:"active_version"`
	ContentHash   *string `json:"content_hash"`
}

const ConditionalActivationCapability = "conditional_activation_v1"
