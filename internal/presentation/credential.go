package presentation

import "sync"

type credentialOwner struct {
	id    uint64
	value string
}

type CredentialRegistry struct {
	mu     sync.RWMutex
	nextID uint64
	owners []credentialOwner
}

func (r *CredentialRegistry) SetDefault(value string) {
	if value == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for index := range r.owners {
		if r.owners[index].id == 0 {
			r.owners[index].value = value
			return
		}
	}
	r.owners = append(r.owners, credentialOwner{value: value})
}

func (r *CredentialRegistry) Clear() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.owners = nil
}

func (r *CredentialRegistry) Register(value string) func() {
	if value == "" {
		return func() {}
	}
	r.mu.Lock()
	r.nextID++
	id := r.nextID
	r.owners = append(r.owners, credentialOwner{id: id, value: value})
	r.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			r.mu.Lock()
			defer r.mu.Unlock()
			for index := range r.owners {
				if r.owners[index].id == id {
					r.owners = append(r.owners[:index], r.owners[index+1:]...)
					return
				}
			}
		})
	}
}

func (r *CredentialRegistry) Values() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	values := make([]string, 0, len(r.owners))
	seen := make(map[string]struct{}, len(r.owners))
	appendValue := func(value string) {
		if value == "" {
			return
		}
		if _, exists := seen[value]; exists {
			return
		}
		seen[value] = struct{}{}
		values = append(values, value)
	}
	for _, owner := range r.owners {
		appendValue(owner.value)
	}
	return values
}

var ActiveCredentials CredentialRegistry
