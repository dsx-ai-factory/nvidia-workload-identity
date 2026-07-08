package jwtsvidutil

import (
	"fmt"

	"github.com/spiffe/go-spiffe/v2/svid/jwtsvid"
)

// SelectByHint returns the only non-nil JWT-SVID with the exact hint.
func SelectByHint(svids []*jwtsvid.SVID, hint string) (*jwtsvid.SVID, error) {
	var match *jwtsvid.SVID
	for _, svid := range svids {
		if svid == nil || svid.Hint != hint {
			continue
		}
		if match != nil {
			return nil, fmt.Errorf("multiple JWT-SVIDs found with hint %q", hint)
		}
		match = svid
	}
	if match == nil {
		return nil, fmt.Errorf("no JWT-SVID found with hint %q", hint)
	}
	return match, nil
}
