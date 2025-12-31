package messages

// ServerStartedMsg indicates the server has started
// Shared between cmd and dashboard packages
type ServerStartedMsg struct {
	Port string
}

// ServerStoppedMsg indicates the server has stopped
type ServerStoppedMsg struct {
	Err error
}
