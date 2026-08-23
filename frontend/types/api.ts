// The single source of truth for API response types is backend/internal/apitypes
// (the Go wire structs). `make gen-types` generates ./api.gen.ts, and this file
// just re-exports it. To change a type, change the Go struct and regenerate
// (do not hand-edit this file or api.gen.ts). `make check` / the CI contract job
// detects any drift.
export * from "./api.gen";
