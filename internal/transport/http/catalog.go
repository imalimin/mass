package httptransport

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"strconv"

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
func registerCatalog(register func(string, bool, endpoint), registerMedia func(string, bool, string, endpoint), registerResourceBearer func(string, endpoint), options IdentityOptions, query func(*http.Request, identity.User) (identity.Query, int, string, error)) {
	svc := options.Catalog
	if svc == nil {
		return
	}
	registerMedia("POST /api/v1/spaces/{space_id}/skills/import", true, "application/zip", func(w http.ResponseWriter, r *http.Request, u identity.User) error {
		space := r.PathValue("space_id")
		if err := options.Service.CheckAccess(r.Context(), u.ID, space); err != nil {
			return err
		}
		draft, err := svc.ImportSkill(r.Context(), u.ID, space, r.URL.Query().Get("filename"), r.Body)
		if err != nil {
			return err
		}
		writeJSON(w, 201, draft)
		return nil
	})
	for _, kind := range []string{"agent", "skill"} {
		base := "/api/v1/spaces/{space_id}/" + kind + "s"
		registerResourceBearer("GET "+base, func(w http.ResponseWriter, r *http.Request, u identity.User) error {
			includeDescription := false
			if kind == "skill" {
				raw := r.URL.Query().Get("include_description")
				if raw != "" && raw != "true" && raw != "false" {
					return identity.ErrInvalid
				}
				includeDescription = raw == "true"
			}
			q, n, scope, e := query(r, u)
			if e != nil {
				return e
			}
			space := r.PathValue("space_id")
			items, e := svc.Repo.CatalogList(r.Context(), u.ID, space, kind, q)
			if e != nil {
				return e
			}
			page := identity.Paginate(options.Service, scope, items, n, func(v catalog.Definition) string { return v.ID })
			if includeDescription {
				for i := range page.Items {
					desc := svc.SkillDescription(r.Context(), u.ID, space, page.Items[i].ID)
					page.Items[i].Description = &desc
				}
			}
			writeJSON(w, 200, page)
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
			v, e := svc.Draft(r.Context(), u.ID, r.PathValue("space_id"), kind, r.PathValue("id"))
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
		register("DELETE "+base+"/{id}", true, func(w http.ResponseWriter, r *http.Request, u identity.User) error {
			if e := svc.Delete(r.Context(), u.ID, r.PathValue("space_id"), kind, r.PathValue("id")); e != nil {
				return e
			}
			w.WriteHeader(204)
			return nil
		})
		if kind == "skill" {
			register("GET "+base+"/{id}/usage", true, func(w http.ResponseWriter, r *http.Request, u identity.User) error {
				items, e := svc.SkillUsage(r.Context(), u.ID, r.PathValue("space_id"), r.PathValue("id"))
				if e != nil {
					return e
				}
				writeJSON(w, 200, map[string]any{"items": items})
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
		}
		for _, debug := range []bool{false, true} {
			if debug && kind != "agent" {
				continue
			}
			path := base + "/{id}/versions"
			if debug {
				path = base + "/{id}/debug-snapshots"
			}
			listVersions := func(w http.ResponseWriter, r *http.Request, u identity.User) error {
				q, n, scope, e := query(r, u)
				if e != nil {
					return e
				}
				items, e := svc.Versions(r.Context(), u.ID, r.PathValue("space_id"), kind, r.PathValue("id"), debug, q)
				if e != nil {
					return e
				}
				writeJSON(w, 200, identity.Paginate(options.Service, scope, items, n, func(v catalog.Version) string { return v.ID }))
				return nil
			}
			if kind == "skill" && !debug {
				registerResourceBearer("GET "+path, listVersions)
			} else {
				register("GET "+path, true, listVersions)
			}
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
			if kind == "skill" && !debug {
				registerResourceBearer("GET "+path+"/{version_id}/package", func(w http.ResponseWriter, r *http.Request, u identity.User) error {
					space := r.PathValue("space_id")
					if err := options.Service.CheckAccess(r.Context(), u.ID, space); err != nil {
						return err
					}
					exported, err := svc.ExportSkillVersionPackage(r.Context(), u.ID, space, r.PathValue("id"), r.PathValue("version_id"))
					if err != nil {
						return err
					}
					defer exported.Close()
					filename := exported.Name + ".zip"
					if exported.Label != "" {
						filename = exported.Name + "-" + exported.Label + ".zip"
					}
					w.Header().Set("Content-Type", "application/zip")
					w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filename}))
					w.Header().Set("Content-Length", strconv.FormatInt(exported.Size, 10))
					w.Header().Set("X-Mass-Skill-Id", exported.SkillID)
					w.Header().Set("X-Mass-Skill-Name", exported.Name)
					w.Header().Set("X-Mass-Skill-Version-Id", exported.VersionID)
					if exported.Label != "" {
						w.Header().Set("X-Mass-Skill-Version", exported.Label)
					}
					w.WriteHeader(200)
					_ = copyAuthorized(r.Context(), w, exported.File, func(ctx context.Context) error {
						return options.Service.CheckAccess(ctx, u.ID, space)
					})
					return nil
				})
			}
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
			register("PUT "+base+"/{id}/visibility", true, func(w http.ResponseWriter, r *http.Request, u identity.User) error {
				var input struct {
					Visibility catalog.Visibility `json:"visibility"`
				}
				if err := decodeBody(w, r, &input); err != nil {
					return err
				}
				definition, err := svc.SetAgentVisibility(r.Context(), u.ID, r.PathValue("space_id"), r.PathValue("id"), input.Visibility)
				if err != nil {
					return err
				}
				writeJSON(w, 200, definition)
				return nil
			})
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
