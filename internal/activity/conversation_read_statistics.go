package activity

// Counters distinguish full byte verification from JSON decoding. They report
// work, not an authority snapshot; every normal permission check stays intact.
type ConversationReadStatistics struct {
	VerifiedReads uint64 `json:"verified_reads"`
	VerifiedBytes uint64 `json:"verified_bytes"`
	Decodes       uint64 `json:"decodes"`
}

func (r *ConversationRegistry) ReadStatistics() ConversationReadStatistics {
	return ConversationReadStatistics{r.verifiedReads.Load(), r.verifiedBytes.Load(), r.decodeCount.Load()}
}
