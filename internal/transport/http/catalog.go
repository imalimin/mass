package httptransport

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"

	"github.com/imalimin/mass/internal/app/catalog"
	"github.com/imalimin/mass/internal/app/identity"
)

func decodeCatalog(w http.ResponseWriter, r *http.Request, v any, limit int64) error {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	d.DisallowUnknownFields()
	if d.Decode(v) != nil {
		return catalog.ErrConfig
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return catalog.ErrConfig
	}
	return nil
}
func registerCatalog(register func(string, bool, endpoint), options IdentityOptions, query func(*http.Request, identity.User) (identity.Query, int, string, error)) {
	svc := options.Catalog
	if svc == nil {
		return
	}
	for _, kind := range []string{"agent", "skill"} {
		base := "/api/v1/spaces/{space_id}/" + kind + "s"
		register("GET "+base, true, func(w http.ResponseWriter, r *http.Request, u identity.User) error {
			q, n, scope, e := query(r, u)
			if e != nil {
				return e
			}
			items, e := svc.Repo.CatalogList(r.Context(), u.ID, r.PathValue("space_id"), kind, q)
			if e != nil {
				return e
			}
			writeJSON(w, 200, identity.Paginate(options.Service, scope, items, n, func(v catalog.Definition) string { return v.ID }))
			return nil
		})
		register("POST "+base, true, func(w http.ResponseWriter, r *http.Request, u identity.User) error {
			var b struct {
				Name string `json:"name"`
			}
			if e := decodeBody(w, r, &b); e != nil {
				return e
			}
			v, e := svc.Create(r.Context(), u.ID, r.PathValue("space_id"), kind, b.Name)
			if e != nil {
				return e
			}
			writeJSON(w, 201, v)
			return nil
		})
		register("POST "+base+"/{id}/copy", true, func(w http.ResponseWriter, r *http.Request, u identity.User) error {
			var b struct {
				Name string `json:"name"`
			}
			if e := decodeBody(w, r, &b); e != nil {
				return e
			}
			v, e := svc.Copy(r.Context(), u.ID, r.PathValue("space_id"), kind, r.PathValue("id"), b.Name)
			if e != nil {
				return e
			}
			writeJSON(w, 201, v)
			return nil
		})
		register("GET "+base+"/{id}/draft", true, func(w http.ResponseWriter, r *http.Request, u identity.User) error {
			v, e := svc.Repo.CatalogDraft(r.Context(), u.ID, r.PathValue("space_id"), kind, r.PathValue("id"))
			if e != nil {
				return e
			}
			writeJSON(w, 200, v)
			return nil
		})
		register("PUT "+base+"/{id}/draft", true, func(w http.ResponseWriter, r *http.Request, u identity.User) error {
			var b struct {
				ExpectedRevision int64            `json:"expected_revision"`
				Document         catalog.Document `json:"document"`
			}
			if e := decodeCatalog(w, r, &b, 1<<20); e != nil {
				return e
			}
			v, e := svc.Save(r.Context(), u.ID, r.PathValue("space_id"), kind, r.PathValue("id"), b.ExpectedRevision, b.Document)
			if e != nil {
				return e
			}
			writeJSON(w, 200, v)
			return nil
		})
		register("POST "+base+"/{id}/resources", true, func(w http.ResponseWriter, r *http.Request, u identity.User) error {
			var b struct {
				ExpectedRevision int64  `json:"expected_revision"`
				Path             string `json:"path"`
				MediaType        string `json:"media_type"`
				Data             string `json:"data_base64"`
			}
			if e := decodeCatalog(w, r, &b, 12<<20); e != nil {
				return e
			}
			raw, e := base64.StdEncoding.Strict().DecodeString(b.Data)
			if e != nil || len(raw) > catalog.MaxResource {
				return catalog.ErrConfig
			}
			v, e := svc.Upload(r.Context(), u.ID, r.PathValue("space_id"), kind, r.PathValue("id"), b.Path, b.MediaType, b.ExpectedRevision, bytes.NewReader(raw))
			if e != nil {
				return e
			}
			writeJSON(w, 200, v)
			return nil
		})
		for _, debug := range []bool{false, true} {
			if debug && kind != "agent" {
				continue
			}
			path := base + "/{id}/versions"
			if debug {
				path = base + "/{id}/debug-snapshots"
			}
			register("GET "+path, true, func(w http.ResponseWriter, r *http.Request, u identity.User) error {
				q, n, scope, e := query(r, u)
				if e != nil {
					return e
				}
				items, e := svc.Repo.CatalogVersions(r.Context(), u.ID, r.PathValue("space_id"), kind, r.PathValue("id"), debug, q)
				if e != nil {
					return e
				}
				writeJSON(w, 200, identity.Paginate(options.Service, scope, items, n, func(v catalog.Version) string { return v.ID }))
				return nil
			})
			register("POST "+path, true, func(w http.ResponseWriter, r *http.Request, u identity.User) error {
				var b struct {
					ExpectedRevision int64  `json:"expected_revision"`
					Label            string `json:"label"`
				}
				if e := decodeBody(w, r, &b); e != nil {
					return e
				}
				v, e := svc.Publish(r.Context(), u.ID, r.PathValue("space_id"), kind, r.PathValue("id"), b.Label, b.ExpectedRevision, debug)
				if e != nil {
					return e
				}
				writeJSON(w, 201, v)
				return nil
			})
			register("GET "+path+"/{version_id}", true, func(w http.ResponseWriter, r *http.Request, u identity.User) error {
				v, e := svc.Version(r.Context(), u.ID, r.PathValue("space_id"), kind, r.PathValue("id"), r.PathValue("version_id"))
				if e != nil {
					return e
				}
				if v.Debug != debug {
					return identity.ErrNotFound
				}
				writeJSON(w, 200, v)
				return nil
			})
		}
		register("GET "+base+"/{id}/versions/{version_id}/references", true, func(w http.ResponseWriter, r *http.Request, u identity.User) error {
			q, n, scope, e := query(r, u)
			if e != nil {
				return e
			}
			items, e := svc.Repo.CatalogReferences(r.Context(), u.ID, r.PathValue("space_id"), kind, r.PathValue("id"), r.PathValue("version_id"), q)
			if e != nil {
				return e
			}
			writeJSON(w, 200, identity.Paginate(options.Service, scope, items, n, func(v catalog.Reference) string { return v.ID }))
			return nil
		})
		register("GET "+base+"/{id}/diff", true, func(w http.ResponseWriter, r *http.Request, u identity.User) error {
			left, e := svc.Version(r.Context(), u.ID, r.PathValue("space_id"), kind, r.PathValue("id"), r.URL.Query().Get("from"))
			if e != nil {
				return e
			}
			right, e := svc.Version(r.Context(), u.ID, r.PathValue("space_id"), kind, r.PathValue("id"), r.URL.Query().Get("to"))
			if e != nil {
				return e
			}
			writeJSON(w, 200, struct {
				From catalog.Version `json:"from"`
				To   catalog.Version `json:"to"`
			}{left, right})
			return nil
		})
		for _, version := range []bool{false, true} {
			path := base + "/{id}/archive"
			action := "archive"
			if version {
				path = base + "/{id}/versions/{version_id}/archive"
				action = "version.archive"
			}
			register("PUT "+path, true, func(w http.ResponseWriter, r *http.Request, u identity.User) error {
				var b struct {
					Archived bool `json:"archived"`
				}
				if e := decodeBody(w, r, &b); e != nil {
					return e
				}
				if e := svc.Repo.CatalogState(r.Context(), u.ID, r.PathValue("space_id"), kind, r.PathValue("id"), r.PathValue("version_id"), action, b.Archived); e != nil {
					return e
				}
				w.WriteHeader(204)
				return nil
			})
		}
		register("DELETE "+base+"/{id}/versions/{version_id}", true, func(w http.ResponseWriter, r *http.Request, u identity.User) error {
			return svc.Repo.CatalogState(r.Context(), u.ID, r.PathValue("space_id"), kind, r.PathValue("id"), r.PathValue("version_id"), "delete", false)
		})
		if kind == "agent" {
			register("PUT "+base+"/{id}/default-version", true, func(w http.ResponseWriter, r *http.Request, u identity.User) error {
				var b struct {
					VersionID string `json:"version_id"`
				}
				if e := decodeBody(w, r, &b); e != nil {
					return e
				}
				if e := svc.Repo.CatalogState(r.Context(), u.ID, r.PathValue("space_id"), kind, r.PathValue("id"), b.VersionID, "default", false); e != nil {
					return e
				}
				w.WriteHeader(204)
				return nil
			})
		}
	}
}
