package manage

import (
	"errors"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// The outcomes an edge (MCP tool, CLI command) distinguishes when an
// operation is refused. ErrNotFound and ErrConflict are the store's own
// values re-exported, the way os.ErrNotExist is fs.ErrNotExist, so a
// caller checks one package. ErrInvalid is manage's own: the spec failed
// validation before anything reached the store.
var (
	ErrNotFound = store.ErrNotFound
	ErrConflict = store.ErrConflict
	ErrInvalid  = errors.New("invalid project spec")
)
