package permissions

import (
	"context"
	"fmt"
	"sync"

	"github.com/rizrmd/dexco/internal/model"
)

type turnIDContextKey struct{}

// ContextWithTurnID lets library tools observe the active turn without changing
// the public Handler signature. Codex stores request_permissions grants in
// per-turn state; Dexco passes the turn id through context so the standalone
// request_permissions tool can record the same scoped grant.
func ContextWithTurnID(ctx context.Context, turnID string) context.Context {
	return context.WithValue(ctx, turnIDContextKey{}, turnID)
}

func TurnIDFromContext(ctx context.Context) string {
	turnID, _ := ctx.Value(turnIDContextKey{}).(string)
	return turnID
}

// Store records Dexco permission grants with Codex's turn/session lifetime
// semantics. It intentionally stores opaque keys rather than filesystem or
// network policy because Dexco is embedded as a library and callers own the
// concrete sandbox or execution policy.
type Store struct {
	mu      sync.Mutex
	session map[string]model.PermissionGrant
	turns   map[string]map[string]model.PermissionGrant
	strict  map[string]map[string]bool
}

func NewStore() *Store {
	return &Store{
		session: make(map[string]model.PermissionGrant),
		turns:   make(map[string]map[string]model.PermissionGrant),
		strict:  make(map[string]map[string]bool),
	}
}

func (s *Store) Record(
	ctx context.Context,
	turnID string,
	grant model.PermissionGrant,
	scope model.PermissionGrantScope,
	strictAutoReview bool,
) error {
	if s == nil {
		return nil
	}
	if grant.Key == "" {
		return fmt.Errorf("record permission grant: empty key")
	}
	if scope == "" {
		scope = model.PermissionGrantScopeTurn
	}
	if turnID == "" {
		turnID = TurnIDFromContext(ctx)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	switch scope {
	case model.PermissionGrantScopeTurn:
		if turnID == "" {
			return fmt.Errorf("record turn permission grant %q: missing turn id", grant.Key)
		}
		if s.turns == nil {
			s.turns = make(map[string]map[string]model.PermissionGrant)
		}
		if s.turns[turnID] == nil {
			s.turns[turnID] = make(map[string]model.PermissionGrant)
		}
		s.turns[turnID][grant.Key] = grant
		if strictAutoReview {
			if s.strict == nil {
				s.strict = make(map[string]map[string]bool)
			}
			if s.strict[turnID] == nil {
				s.strict[turnID] = make(map[string]bool)
			}
			s.strict[turnID][grant.Key] = true
		} else if strictTurnGrants := s.strict[turnID]; strictTurnGrants != nil {
			// Codex's strict auto-review flag belongs to the granted
			// additional-permission profile for the current turn. Keep Dexco's
			// opaque grant-key adaptation equally narrow: re-recording the same
			// key without strict review clears only that key and does not force
			// unrelated session or turn grants through the reviewer.
			delete(strictTurnGrants, grant.Key)
			if len(strictTurnGrants) == 0 {
				delete(s.strict, turnID)
			}
		}
		return nil
	case model.PermissionGrantScopeSession:
		if s.session == nil {
			s.session = make(map[string]model.PermissionGrant)
		}
		s.session[grant.Key] = grant
		return nil
	default:
		return fmt.Errorf("record permission grant %q: unknown scope %q", grant.Key, scope)
	}
}

func (s *Store) Has(turnID string, key string) bool {
	if s == nil || key == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.session[key]; ok {
		return true
	}
	if turnID == "" {
		return false
	}
	turnGrants := s.turns[turnID]
	_, ok := turnGrants[key]
	return ok
}

func (s *Store) StrictAutoReview(turnID string) bool {
	if s == nil || turnID == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.strict[turnID]) > 0
}

func (s *Store) StrictAutoReviewForGrant(turnID string, key string) bool {
	if s == nil || turnID == "" || key == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.strict[turnID][key]
}

func (s *Store) ClearTurn(turnID string) {
	if s == nil || turnID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.turns, turnID)
	delete(s.strict, turnID)
}
