package httpserver

import (
	"context"
	"database/sql"
	"errors"
	"maps"
	"net/http"
	"strconv"

	"budget-tracking/backend/internal/catalog"
	"budget-tracking/backend/internal/database/dbgen"
	"budget-tracking/backend/internal/ledger"
)

type entityView struct {
	ID               int64    `json:"id"`
	Name             string   `json:"name"`
	TransactionCount int64    `json:"transactionCount"`
	Aliases          []string `json:"aliases"`
	// System marks Uncategorized, which cannot be renamed or merged away.
	System bool `json:"system,omitempty"`
	// Rule fields are set for payees with a categorization rule.
	RuleID           *int64 `json:"ruleId,omitempty"`
	RuleCategoryID   *int64 `json:"ruleCategoryId,omitempty"`
	RuleCategoryName string `json:"ruleCategoryName,omitempty"`
}

type ruleView struct {
	ID           int64  `json:"id"`
	PayeeID      int64  `json:"payeeId"`
	PayeeName    string `json:"payeeName"`
	CategoryID   int64  `json:"categoryId"`
	CategoryName string `json:"categoryName"`
	Enabled      bool   `json:"enabled"`
}

func pathID(w http.ResponseWriter, r *http.Request, name string) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusNotFound, name+" not found")
		return 0, false
	}
	return id, true
}

func aliasesOrEmpty(aliases map[int64][]string, id int64) []string {
	if list := aliases[id]; list != nil {
		return list
	}
	return []string{}
}

func (a *api) listCategories(w http.ResponseWriter, r *http.Request, _ *session) {
	rows, err := a.store.Queries.ListCategoryRows(r.Context())
	if err != nil {
		internalError(w, "list categories")
		return
	}
	aliases, err := catalog.Categories.Aliases(r.Context(), a.store.DB)
	if err != nil {
		internalError(w, "list category aliases")
		return
	}
	views := make([]entityView, 0, len(rows))
	for _, row := range rows {
		views = append(views, entityView{ID: row.ID, Name: row.Name, TransactionCount: row.TransactionCount,
			Aliases: aliasesOrEmpty(aliases, row.ID), System: row.NormalizedName == "uncategorized"})
	}
	writeJSON(w, http.StatusOK, map[string][]entityView{"categories": views})
}

// payeeSorts are the payee list's sort keys. Payees without a rule sort last.
var payeeSorts = map[string]string{
	"name":             "p.name COLLATE NOCASE",
	"rule":             "c.name COLLATE NOCASE",
	"transactionCount": "(SELECT count(*) FROM transactions t WHERE t.payee_id = p.id AND t.deleted_at IS NULL)",
}

func (a *api) listPayees(w http.ResponseWriter, r *http.Request, _ *session) {
	values := r.URL.Query()
	sort, direction, fields := "name", "asc", map[string]string{}
	parseSort(values, payeeSorts, &sort, &direction, fields)
	values.Del("sort")
	values.Del("dir")
	filter, filterFields := parseRegisterFilter(values)
	maps.Copy(fields, filterFields)
	if len(fields) > 0 {
		writeFieldErrors(w, fields)
		return
	}
	search := ledger.NormalizeName(filter.search)
	total, err := a.store.Queries.CountPayees(r.Context(), search)
	if err != nil {
		internalError(w, "count payees")
		return
	}
	ids, err := pageIDs(r.Context(), a.store.DB, `SELECT p.id FROM payees p
LEFT JOIN categorization_rules r ON r.payee_id = p.id LEFT JOIN categories c ON c.id = r.category_id
WHERE ? = '' OR instr(p.normalized_name, ?) > 0
ORDER BY `+payeeSorts[sort]+" "+direction+" NULLS LAST, p.name COLLATE NOCASE, p.id LIMIT ? OFFSET ?",
		search, search, filter.pageSize, (filter.page-1)*filter.pageSize)
	if err != nil {
		internalError(w, "list payees")
		return
	}
	rows, err := a.store.Queries.ListPayeeRows(r.Context(), ids)
	if err != nil {
		internalError(w, "list payees")
		return
	}
	aliases, err := catalog.Payees.Aliases(r.Context(), a.store.DB)
	if err != nil {
		internalError(w, "list payee aliases")
		return
	}
	views := make([]entityView, 0, len(rows))
	for _, row := range inOrder(ids, rows, func(row dbgen.ListPayeeRowsRow) int64 { return row.ID }) {
		views = append(views, entityView{ID: row.ID, Name: row.Name, TransactionCount: row.TransactionCount,
			Aliases: aliasesOrEmpty(aliases, row.ID), RuleID: nullID(row.RuleID), RuleCategoryID: nullID(row.RuleCategoryID),
			RuleCategoryName: row.RuleCategoryName.String})
	}
	writeJSON(w, http.StatusOK, map[string]any{"payees": views, "total": total, "page": filter.page, "pageSize": filter.pageSize})
}

func catalogFailure(w http.ResponseWriter, err error, operation string) {
	switch {
	case errors.Is(err, catalog.ErrInvalidName):
		writeFieldErrors(w, map[string]string{"name": "Enter a name of up to 100 characters"})
	case errors.Is(err, catalog.ErrNameTaken):
		writeFieldErrors(w, map[string]string{"name": "Another entry already uses this name. Merge them instead."})
	case errors.Is(err, catalog.ErrSameEntry):
		writeFieldErrors(w, map[string]string{"targetId": "Choose a different entry"})
	case errors.Is(err, catalog.ErrProtected):
		writeError(w, http.StatusConflict, "Uncategorized cannot be renamed or merged away")
	case errors.Is(err, catalog.ErrNotFound):
		writeError(w, http.StatusNotFound, "Entry not found")
	default:
		writeFailure(w, err, operation)
	}
}

// createEntity returns the existing entry for an equivalent name instead of
// creating a second one.
func createEntity(kind catalog.Kind) func(*api, http.ResponseWriter, *http.Request, *session) {
	return func(a *api, w http.ResponseWriter, r *http.Request, _ *session) {
		var input struct {
			Name string `json:"name"`
		}
		if !decodeJSON(w, r, &input) {
			return
		}
		var id int64
		var created bool
		err := a.inTx(r.Context(), func(q txn) error {
			var err error
			id, created, err = kind.Resolve(r.Context(), q, input.Name)
			return err
		})
		if err != nil {
			catalogFailure(w, err, "create entry")
			return
		}
		status := http.StatusOK
		if created {
			status = http.StatusCreated
		}
		name, err := a.entityName(r.Context(), kind, id)
		if err != nil {
			internalError(w, "load entry")
			return
		}
		writeJSON(w, status, entityView{ID: id, Name: name, Aliases: []string{}})
	}
}

func (a *api) entityName(ctx context.Context, kind catalog.Kind, id int64) (string, error) {
	if kind == catalog.Payees {
		payee, err := a.store.Queries.GetPayee(ctx, id)
		return payee.Name, err
	}
	category, err := a.store.Queries.GetCategory(ctx, id)
	return category.Name, err
}

func renameEntity(kind catalog.Kind) func(*api, http.ResponseWriter, *http.Request, *session) {
	return func(a *api, w http.ResponseWriter, r *http.Request, _ *session) {
		id, ok := pathID(w, r, "Entry")
		if !ok {
			return
		}
		var input struct {
			Name string `json:"name"`
		}
		if !decodeJSON(w, r, &input) {
			return
		}
		err := a.inTx(r.Context(), func(q txn) error {
			return kind.Rename(r.Context(), q, id, input.Name)
		})
		if err != nil {
			catalogFailure(w, err, "rename entry")
			return
		}
		name, err := a.entityName(r.Context(), kind, id)
		if err != nil {
			internalError(w, "load entry")
			return
		}
		writeJSON(w, http.StatusOK, entityView{ID: id, Name: name, Aliases: []string{}})
	}
}

func mergeEntity(kind catalog.Kind) func(*api, http.ResponseWriter, *http.Request, *session) {
	return func(a *api, w http.ResponseWriter, r *http.Request, _ *session) {
		id, ok := pathID(w, r, "Entry")
		if !ok {
			return
		}
		var input struct {
			TargetID int64 `json:"targetId"`
		}
		if !decodeJSON(w, r, &input) {
			return
		}
		err := a.inTx(r.Context(), func(q txn) error {
			return kind.Merge(r.Context(), q, id, input.TargetID)
		})
		if err != nil {
			catalogFailure(w, err, "merge entries")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func (a *api) listRules(w http.ResponseWriter, r *http.Request, _ *session) {
	rows, err := a.store.Queries.ListRules(r.Context())
	if err != nil {
		internalError(w, "list rules")
		return
	}
	views := make([]ruleView, 0, len(rows))
	for _, row := range rows {
		views = append(views, ruleView{ID: row.ID, PayeeID: row.PayeeID.Int64, PayeeName: row.PayeeName,
			CategoryID: row.CategoryID, CategoryName: row.CategoryName, Enabled: row.Enabled == 1})
	}
	writeJSON(w, http.StatusOK, map[string][]ruleView{"rules": views})
}

// saveRule creates or replaces a payee's rule. Names create missing payees
// and categories, so an explicit local rule can introduce a category.
// Existing transactions keep their categories.
func (a *api) saveRule(w http.ResponseWriter, r *http.Request, _ *session) {
	var input struct {
		PayeeID    int64  `json:"payeeId"`
		Payee      string `json:"payee"`
		CategoryID int64  `json:"categoryId"`
		Category   string `json:"category"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	var id int64
	err := a.inTx(r.Context(), func(q txn) error {
		fields := map[string]string{}
		payee, err := resolveEntity(r.Context(), q, catalog.Payees, input.PayeeID, input.Payee)
		if err != nil {
			return err
		}
		if payee == 0 {
			fields["payee"] = "Choose or enter a payee"
		}
		category, err := resolveEntity(r.Context(), q, catalog.Categories, input.CategoryID, input.Category)
		if err != nil {
			return err
		}
		if category == 0 {
			fields["category"] = "Choose or enter a category"
		}
		if len(fields) > 0 {
			return fieldError(fields)
		}
		id, err = catalog.SetRule(r.Context(), q, payee, category)
		return err
	})
	if err != nil {
		writeFailure(w, err, "save rule")
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"id": id})
}

// resolveEntity accepts an existing ID or a name. It returns 0 when neither
// identifies a valid entry.
func resolveEntity(ctx context.Context, q txn, kind catalog.Kind, id int64, name string) (int64, error) {
	if id != 0 {
		found, err := kind.Exists(ctx, q, id)
		if err != nil || !found {
			return 0, err
		}
		return id, nil
	}
	resolved, _, err := kind.Resolve(ctx, q, name)
	if errors.Is(err, catalog.ErrInvalidName) {
		return 0, nil
	}
	return resolved, err
}

func (a *api) updateRule(w http.ResponseWriter, r *http.Request, _ *session) {
	id, ok := pathID(w, r, "Rule")
	if !ok {
		return
	}
	var input struct {
		CategoryID int64 `json:"categoryId"`
		Enabled    bool  `json:"enabled"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	err := a.inTx(r.Context(), func(q txn) error {
		if _, err := q.GetRule(r.Context(), id); errors.Is(err, sql.ErrNoRows) {
			return &requestError{status: http.StatusNotFound, message: "Rule not found"}
		} else if err != nil {
			return err
		}
		found, err := catalog.Categories.Exists(r.Context(), q, input.CategoryID)
		if err != nil {
			return err
		}
		if !found {
			return fieldError(map[string]string{"categoryId": "Choose a category"})
		}
		return q.UpdateRule(r.Context(), dbgen.UpdateRuleParams{CategoryID: input.CategoryID, Enabled: boolInt(input.Enabled), ID: id})
	})
	if err != nil {
		writeFailure(w, err, "update rule")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *api) deleteRule(w http.ResponseWriter, r *http.Request, _ *session) {
	id, ok := pathID(w, r, "Rule")
	if !ok {
		return
	}
	if err := a.store.Queries.DeleteRule(r.Context(), id); err != nil {
		internalError(w, "delete rule")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
