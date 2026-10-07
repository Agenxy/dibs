package core

// MessageAccess is the read_mail and history creation/adoption fence. Serial
// is the conversation's lookup key; it remains authoritative even when a
// fixture or legacy message has no embedded Serial. A history reader must
// supply the latest committed ownership header, even for an earlier unit.
// The second result hides parties belonging to a previous incarnation.
func MessageAccess(serial uint64, m *Message, reader *Agent) (allowed, inherited bool) {
	if m == nil || reader == nil {
		return false, false
	}
	adopted := m.To == reader.ID && adoptedForReader(m, reader)
	inherited = reader.CreatedSerial > 0 && serial < reader.CreatedSerial && !adopted
	return !inherited && (m.From == reader.ID || m.To == reader.ID), inherited
}

func adoptedForReader(m *Message, reader *Agent) bool {
	if m.AdoptedFrom == "" {
		return false
	}
	return reader == nil || reader.CreatedSerial == 0 || m.AdoptedAt >= reader.CreatedSerial
}
