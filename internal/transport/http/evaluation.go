package httptransport

import (
	"net/http"
	"strconv"

	"github.com/imalimin/mass/internal/app/evaluation"
	"github.com/imalimin/mass/internal/app/identity"
	"github.com/imalimin/mass/internal/domain/storage"
)

func registerEvaluation(register func(string, bool, endpoint), options IdentityOptions, query func(*http.Request, identity.User) (identity.Query, int, string, error)) {
	svc := options.Evaluation
	if svc == nil {
		return
	}
	base := "/api/v1/spaces/{space_id}/evaluation"
	register("GET "+base+"/datasets", true, func(w http.ResponseWriter, r *http.Request, u identity.User) error {
		q, n, scope, err := query(r, u)
		if err != nil {
			return err
		}
		items, err := svc.Repo.EvaluationDatasets(r.Context(), u.ID, r.PathValue("space_id"), q)
		if err != nil {
			return err
		}
		writeJSON(w, 200, identity.Paginate(options.Service, scope, items, n, func(v evaluation.Dataset) string { return v.ID }))
		return nil
	})
	save := func(w http.ResponseWriter, r *http.Request, u identity.User) error {
		var in evaluation.DatasetInput
		if err := decodeCatalog(w, r, &in, 1<<20); err != nil {
			return err
		}
		v, err := svc.SaveDataset(r.Context(), u.ID, r.PathValue("space_id"), r.PathValue("dataset_id"), in)
		if err != nil {
			return err
		}
		writeJSON(w, 201, v)
		return nil
	}
	register("POST "+base+"/datasets", true, save)
	register("POST "+base+"/datasets/{dataset_id}/versions", true, save)
	read := func(w http.ResponseWriter, r *http.Request, u identity.User) error {
		var version int64
		if value := r.PathValue("version"); value != "" {
			n, err := strconv.ParseInt(value, 10, 64)
			if err != nil || n < 1 {
				return identity.ErrInvalid
			}
			version = n
		}
		if !storage.ValidShortID(r.PathValue("dataset_id")) {
			return identity.ErrInvalid
		}
		v, err := svc.Repo.EvaluationDataset(r.Context(), u.ID, r.PathValue("space_id"), r.PathValue("dataset_id"), version)
		if err != nil {
			return err
		}
		writeJSON(w, 200, v)
		return nil
	}
	register("GET "+base+"/datasets/{dataset_id}", true, read)
	register("GET "+base+"/datasets/{dataset_id}/versions/{version}", true, read)
	register("POST "+base+"/experiments", true, func(w http.ResponseWriter, r *http.Request, u identity.User) error {
		var in evaluation.ExperimentInput
		if err := decodeBody(w, r, &in); err != nil {
			return err
		}
		v, err := svc.CreateExperiment(r.Context(), u.ID, r.PathValue("space_id"), in)
		if err != nil {
			return err
		}
		writeJSON(w, 202, v)
		return nil
	})
	register("GET "+base+"/experiments", true, func(w http.ResponseWriter, r *http.Request, u identity.User) error {
		q, n, scope, err := query(r, u)
		if err != nil {
			return err
		}
		items, err := svc.Experiments(r.Context(), u.ID, r.PathValue("space_id"), q)
		if err != nil {
			return err
		}
		writeJSON(w, 200, identity.Paginate(options.Service, scope, items, n, func(v evaluation.Experiment) string { return v.ID }))
		return nil
	})
	register("GET "+base+"/experiments/{experiment_id}", true, func(w http.ResponseWriter, r *http.Request, u identity.User) error {
		v, err := svc.Experiment(r.Context(), u.ID, r.PathValue("space_id"), r.PathValue("experiment_id"))
		if err != nil {
			return err
		}
		writeJSON(w, 200, v)
		return nil
	})
	register("GET "+base+"/experiments/{experiment_id}/cases", true, func(w http.ResponseWriter, r *http.Request, u identity.User) error {
		q, n, scope, err := query(r, u)
		if err != nil {
			return err
		}
		after := 0
		if q.After != "" {
			after, err = strconv.Atoi(q.After)
			if err != nil || after < 0 {
				return identity.ErrInvalid
			}
		}
		items, err := svc.Cases(r.Context(), u.ID, r.PathValue("space_id"), r.PathValue("experiment_id"))
		if err != nil {
			return err
		}
		filtered := []evaluation.CaseView{}
		for _, v := range items {
			if v.Ordinal > after {
				filtered = append(filtered, v)
			}
			if len(filtered) >= q.Limit {
				break
			}
		}
		writeJSON(w, 200, identity.Paginate(options.Service, scope, filtered, n, func(v evaluation.CaseView) string { return strconv.Itoa(v.Ordinal) }))
		return nil
	})
	register("GET "+base+"/experiments/{experiment_id}/cases/{ordinal}", true, func(w http.ResponseWriter, r *http.Request, u identity.User) error {
		ordinal, err := strconv.Atoi(r.PathValue("ordinal"))
		if err != nil || ordinal < 1 {
			return identity.ErrInvalid
		}
		items, err := svc.Cases(r.Context(), u.ID, r.PathValue("space_id"), r.PathValue("experiment_id"))
		if err != nil {
			return err
		}
		if ordinal > len(items) {
			return identity.ErrNotFound
		}
		writeJSON(w, 200, items[ordinal-1])
		return nil
	})
}
