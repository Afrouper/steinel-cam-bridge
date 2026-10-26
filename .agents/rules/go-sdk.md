---
trigger: always_on
description: "Erlaubt Go- und CGO-Befehle (direkt sowie mit DYLD_LIBRARY_PATH / DYNLIB_PATH)"
triggers:
  - glob: "**/*"
permissions:
  allow:
    # --- Go & CGO Befehle (direkt ohne Env-Variablen) ---
    - "command(regex:^go (test|build|run|doc|version|vet|get|list|clean|tool|env).*)"
    - "command(regex:^go mod (tidy|download|verify|vendor|graph|why|edit).*)"
    - "command(regex:^go mod.*)"
    - "command(regex:^go fmt.*)"
    - "command(gofmt)"
    - "command(regex:^gofmt .*)"
    - "command(regex:^golangci-lint.*)"

    # --- Go Befehle mit vorangestelltem Library-Pfad (DYLD_LIBRARY_PATH, DYNLIB_PATH, LD_LIBRARY_PATH) ---
    - "command(regex:^(DYLD_LIBRARY_PATH|DYNLIB_PATH|LD_LIBRARY_PATH)=.+go (test|build|run|doc|version|vet|get|list).*)"
    - "command(regex:^(DYLD_LIBRARY_PATH|DYNLIB_PATH|LD_LIBRARY_PATH)="(\$\(pwd\)|\`pwd\`|[^\s"]+)"\s+go (test|build|run|doc|version|vet|get|list).*)"

    # --- Go Befehle mit CGO-Flags oder kombinierten Umgebungsvariablen ---
    - "command(regex:^(env\s+)?([A-Za-z0-9_]+=[^\s]+\s+)*go (test|build|run|doc|version|vet|get|list|mod|fmt).*)"
    - "command(regex:^.*go (test|build|doc|version|vet|get|list|run).*)"
---

# Go & CGO Projekt-Richtlinien

Dieses Projekt verwendet ein lokales C-SDK im Ordner `.sdk/`. 
Der Agent darf Go-Builds, Go-Tests und Go-Tools sowohl **direkt** (ohne gesetzte Variablen) als auch mit **gesetztem Library-Pfad** (`DYLD_LIBRARY_PATH`, `DYNLIB_PATH` oder `LD_LIBRARY_PATH`) sowie CGO-Flags autonom ausführen.

### Richtlinien zur Ausführung:
- **Mit Library-Pfad (CGO & Nabto C-SDK):**
  Stelle sicher, dass bei Testausführungen und Builds mit CGO der Library-Pfad auf das lokale SDK zeigt:
  ```bash
  DYLD_LIBRARY_PATH="$(pwd)/.sdk/lib" go test ./pkg/nabto/...
  ```
  Nutze beim Kompilieren bei Bedarf die CGO-Flags für `.sdk/lib` und `.sdk/include`.
- **Ohne Library-Pfad (Pure Go):**
  Packages, die kein CGO benötigen (z. B. `pkg/nabtopure`, `pkg/xiongmai`, `pkg/rtsp`, `pkg/audio`, `pkg/storage`, `pkg/config`), können direkt ohne gesetzte Umgebungsvariablen getestet und gebaut werden:
  ```bash
  go test ./pkg/nabtopure/...
  go build ./cmd/launcher
  ```
- Go-Modul-Wartung (`go mod tidy`, `gofmt`, `golangci-lint`) ist uneingeschränkt freigegeben.
