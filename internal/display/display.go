package display

import (
	"strconv"

	"github.com/darvell/blob/internal/api"
)

func ResourceUsage(u api.ResourceUsage, suffix string) string {
	out := strconv.Itoa(u.Reserved) + "/" + strconv.Itoa(u.Available) + "/" + strconv.Itoa(u.Total)
	if suffix != "" {
		out += suffix
	}
	return out
}

func ShortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
