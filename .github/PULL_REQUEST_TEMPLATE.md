## What changed

Describe the user-visible outcome and why this approach fits Kerneon.

## Evidence

- [ ] `go test -count=1 ./...`
- [ ] Relevant security and rollback boundaries were reviewed
- [ ] User-facing claims distinguish measurement from inference
- [ ] Screenshots or benchmark windows are attached when UI/performance changed

## Safety and restoration

List every state mutation introduced or changed, its capability gate, its journal entry and its exact return path. Write “None” when the change is read-only.
