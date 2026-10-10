package schemas

// BareSHA256Hex is the bare 64-hex lowercase sha256 pattern (OD-R42: no
// 'sha256:' prefix), a leaf definition shared by doc-patch and claim-proposal.
func IsBareSHA256Hex(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < 64; i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}
