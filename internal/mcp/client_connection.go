package mcp

func (s *Server) ObserveClientRequest(key string, authorized bool) func() {
	if s == nil || s.runtime == nil {
		return func() {}
	}
	return s.runtime.ObserveClientRequest(key, authorized)
}
