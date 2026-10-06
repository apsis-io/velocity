//go:build velocitydebug

package ownership

import "log/slog"

// logNetDrop says out loud that the net, not a caller, ran a Drop. Debug
// builds only: in production the net is silent, because a backstop doing its
// job is a bug report waiting to be written, not an incident.
func logNetDrop() {
	slog.Warn("velocity ownership drop ran by the unreachable net, not a release")
}
