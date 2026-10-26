package runcontext

import (
	"context"
	"reflect"
	"sync"
)

// StoredChatScope is supplied as both Input.Chat and DurableRunInput.Context. Reads restore a
// detached view; the original RunWriter joint CAS must acknowledge before any dispatch.
type StoredChatScope struct {
	mu                 sync.Mutex
	store              HistoryRunStore
	chatID             string
	chat               *ChatContext
	expected           *string
	bound              *HistoryContextRef
	manifest           HistoryObjectRef
	resume             *int
	opening            ChatScope
	turn               *ChatTurn
	attached           bool
	closed             bool
	privateLedgerScope string
	phaseError         error
}

func NewStoredChatScope(store HistoryRunStore, chatID string, chat *ChatContext, head *ContextSnapshotHead) (*StoredChatScope, error) {
	if store == nil || store.HistoryVersion() != 1 || !validStorageID(chatID) || chat == nil || head != nil && head.ChatID != chatID {
		return nil, &ContextTurnBindingInvalid{}
	}
	out := &StoredChatScope{store: store, chatID: chatID, chat: chat}
	if head != nil {
		revision := head.Revision
		out.expected = &revision
		out.manifest = head.Manifest
	}
	return out, nil
}
func (s *StoredChatScope) Context() *ChatContext { return s.chat }
func (s *StoredChatScope) LedgerScope() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.privateLedgerScope
}
func (s *StoredChatScope) attachPrivate() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.privateLedgerScope == "" || !s.closed || s.attached {
		return &ContextTurnBindingInvalid{}
	}
	s.attached = true
	return nil
}
func (s *StoredChatScope) rejectPhase() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.phaseError = &ContextTurnBindingInvalid{}
}
func (s *StoredChatScope) enterPrivate(scope string, turn *ChatTurn) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	number := 0
	if s.turn != nil {
		number = s.turn.number
	} else if s.resume != nil {
		number = *s.resume
	}
	if number == 0 || s.turn != turn || s.chat.hasActiveTurn(number) || scope == s.chat.Scope() {
		return &ContextTurnBindingInvalid{}
	}
	s.closed = true
	s.privateLedgerScope = scope
	return nil
}
func (s *StoredChatScope) OpenTurn() (*ChatTurn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.attached || s.closed {
		return nil, &ContextTurnBindingInvalid{}
	}
	s.attached = true
	if s.turn != nil {
		return s.turn, nil
	}
	var err error
	if s.opening != nil {
		s.turn, err = s.opening.OpenTurn()
	} else {
		s.turn, err = s.chat.OpenTurn()
	}
	return s.turn, err
}
func (s *StoredChatScope) matches(saved *HistoryContextRef) bool {
	return saved != nil && s.bound != nil && historyInputText(saved) == historyInputText(s.bound)
}

// suspend ends only this invocation attachment; a paused logical turn remains active.
func (s *StoredChatScope) suspend(turn *ChatTurn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.turn != turn || turn == nil && s.privateLedgerScope == "" {
		return false
	}
	s.attached = false
	return !s.closed
}

// Close acknowledges actual logical-turn release for a direct host-owned closed invocation.
// It does not terminalize the run checkpoint or grant execution authority.
func (s *StoredChatScope) Close(ctx context.Context) (ContextSnapshotHead, error) {
	s.mu.Lock()
	attached := s.attached
	turn := s.turn
	s.mu.Unlock()
	if attached || turn == nil && s.LedgerScope() == "" {
		return ContextSnapshotHead{}, &ContextTurnBindingInvalid{}
	}
	update, err := s.prepare(ctx, true)
	if err != nil {
		return ContextSnapshotHead{}, err
	}
	head, err := s.store.CommitContextSnapshot(ctx, update, newStorageIdentity())
	if err != nil {
		return ContextSnapshotHead{}, err
	}
	expected := ""
	if update.ExpectedRevision != nil {
		expected = *update.ExpectedRevision
	}
	if head.ChatID != s.chatID || !validStorageID(head.Revision) || head.Revision == expected || head.Manifest != update.Manifest {
		return ContextSnapshotHead{}, &StorageAcknowledgementInvalid{}
	}
	s.mu.Lock()
	s.expected = &head.Revision
	s.mu.Unlock()
	return head, nil
}
func sameHistoryStore(a, b RunStore) bool {
	if a == nil || b == nil {
		return false
	}
	av, bv := reflect.ValueOf(a), reflect.ValueOf(b)
	return av.Type() == bv.Type() && av.Comparable() && av.Interface() == bv.Interface()
}
func (s *StoredChatScope) prepare(ctx context.Context, terminal ...bool) (ContextSnapshotUpdate, error) {
	closed := len(terminal) > 0 && terminal[0]
	return s.prepareForCheckpoint(ctx, closed, nil)
}
func (s *StoredChatScope) prepareForCheckpoint(ctx context.Context, terminal bool, scope *string) (ContextSnapshotUpdate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.phaseError != nil {
		return ContextSnapshotUpdate{}, s.phaseError
	}
	if scope != nil {
		expected := s.privateLedgerScope
		if expected == "" {
			expected = s.chat.Scope()
		}
		if *scope != expected {
			return ContextSnapshotUpdate{}, &ContextTurnBindingInvalid{}
		}
	}
	if s.turn == nil && s.resume == nil {
		return ContextSnapshotUpdate{}, &ContextTurnBindingInvalid{}
	}
	var raw []byte
	var err error
	if terminal && !s.closed {
		s.closed = true
		raw, err = s.chat.snapshotClosing(s.turn)
	} else {
		raw, err = s.chat.Snapshot()
	}
	if err != nil {
		return ContextSnapshotUpdate{}, err
	}
	manifest, objects, err := ChunkHistoryValue(raw)
	if err != nil {
		return ContextSnapshotUpdate{}, err
	}
	objects = append(objects, manifest)
	batch := []HistoryObject{}
	size := 0
	for _, object := range objects {
		if size+object.Bytes > StorageTransactionBytes {
			if err = s.store.StageHistoryObjects(ctx, HistoryResource{Kind: "chat", ID: s.chatID}, batch); err != nil {
				return ContextSnapshotUpdate{}, err
			}
			batch = []HistoryObject{}
			size = 0
		}
		batch = append(batch, object)
		size += object.Bytes
	}
	if len(batch) > 0 {
		if err = s.store.StageHistoryObjects(ctx, HistoryResource{Kind: "chat", ID: s.chatID}, batch); err != nil {
			return ContextSnapshotUpdate{}, err
		}
	}
	turn := 0
	if s.turn != nil {
		turn = s.turn.number
	} else {
		turn = *s.resume
	}
	return ContextSnapshotUpdate{ChatID: s.chatID, ExpectedRevision: historyClone(s.expected), Manifest: manifest.HistoryObjectRef, Turn: &turn, Closed: s.closed, PrivateScope: s.privateLedgerScope}, nil
}
func (s *StoredChatScope) acknowledge(saved *HistoryContextRef) {
	s.mu.Lock()
	defer s.mu.Unlock()
	revision := saved.Revision
	s.expected = &revision
	s.manifest = saved.Manifest
}
func ReadStoredChatSnapshot(ctx context.Context, store HistoryRunStore, head ContextSnapshotHead) ([]byte, error) {
	read := func(ref HistoryObjectRef) ([]byte, error) {
		if !validHistoryRef(ref) {
			return nil, &HistoryInputInvalid{}
		}
		page, err := store.ReadHistoryObject(ctx, HistoryResource{Kind: "chat", ID: head.ChatID}, head.Revision, ref.Digest, 0, HistoryObjectBytes)
		if err != nil {
			return nil, err
		}
		if page.Digest != ref.Digest || page.Total != ref.Bytes || page.Offset != 0 || page.NextOffset != ref.Bytes || page.HasMore || len(page.Value) != ref.Bytes || HistoryBytesDigest(page.Value) != ref.Digest {
			return nil, &HistoryInputInvalid{}
		}
		return page.Value, nil
	}
	raw, err := read(head.Manifest)
	if err != nil {
		return nil, err
	}
	manifest, err := readHistoryChunkManifest(raw)
	if err != nil {
		return nil, err
	}
	full := make([]byte, 0, manifest.Bytes)
	for _, ref := range manifest.Parts {
		part, err := read(ref)
		if err != nil {
			return nil, err
		}
		full = append(full, part...)
	}
	if len(full) != manifest.Bytes || HistoryBytesDigest(full) != manifest.Digest {
		return nil, &HistoryInputInvalid{}
	}
	if _, err = ReadChatContextSnapshot(full); err != nil {
		return nil, err
	}
	return full, nil
}
func RestoreStoredChatScope(ctx context.Context, store HistoryRunStore, saved HistoryContextRef) (*StoredChatScope, error) {
	if saved.Turn == nil {
		return nil, &ContextTurnBindingInvalid{}
	}
	latest, err := store.LoadContextSnapshot(ctx, saved.ChatID)
	if err != nil {
		return nil, err
	}
	if latest == nil || latest.ChatID != saved.ChatID {
		return nil, &ContextTurnBindingInvalid{}
	}
	raw, err := ReadStoredChatSnapshot(ctx, store, *latest)
	if err != nil {
		return nil, err
	}
	chat, err := RestoreChatContext(raw)
	if err != nil {
		return nil, err
	}
	source, err := NewStoredChatScope(store, saved.ChatID, chat, latest)
	if err != nil {
		return nil, err
	}
	source.bound = historyClone(&saved)
	source.resume = historyClone(saved.Turn)
	if saved.PrivateScope != "" {
		if !saved.Closed || saved.PrivateScope == chat.Scope() || chat.hasActiveTurn(*saved.Turn) {
			return nil, &ContextTurnBindingInvalid{}
		}
		source.closed = true
		source.privateLedgerScope = saved.PrivateScope
		return source, nil
	}
	if saved.Closed {
		return nil, &ContextTurnBindingInvalid{}
	}
	source.opening, err = chat.ResumeTurn(*saved.Turn)
	if err != nil {
		return nil, err
	}
	return source, nil
}
