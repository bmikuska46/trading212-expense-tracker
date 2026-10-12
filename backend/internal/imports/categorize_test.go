package imports

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"budget-tracking/backend/internal/integrations"
)

func TestAskCategoriesChunksAndKeepsConfidentPicks(t *testing.T) {
	payees := make([]newPayee, categoryChunk+3)
	for index := range payees {
		payees[index] = newPayee{key: fmt.Sprintf("payee %d", index), name: fmt.Sprintf("Payee %d", index), direction: "money out"}
	}
	options := []categoryOption{{id: 7, name: "Dining"}, {id: 9, name: "Groceries"}}
	var sizes []int
	ask := func(_ context.Context, _ any, questions map[string]integrations.Question) (map[string]integrations.Answer, error) {
		sizes = append(sizes, len(questions))
		if len(sizes) == 2 {
			return nil, errors.New("unavailable")
		}
		return map[string]integrations.Answer{
			"payee 1": {Choice: "category 2", Confidence: 0.8},
			"payee 2": {Choice: "category 1", Confidence: 0.2},
			"payee 3": {Choice: "category 3", Confidence: 0.9},
			"payee 4": {Choice: noCategory, Confidence: 0.9},
		}, nil
	}
	picks, err := askCategories(context.Background(), ask, payees, options)
	if err == nil || len(sizes) != 2 || sizes[0] != categoryChunk || sizes[1] != 3 {
		t.Fatalf("chunks: %v %v", sizes, err)
	}
	if len(picks) != 1 || picks["payee 0"] != (pick{category: 9, confidence: 0.8}) {
		t.Fatalf("only confident picks among the options are kept, including those answered before an error: %+v", picks)
	}

	many := make([]categoryOption, maxChoiceOptions)
	asked := false
	none := func(context.Context, any, map[string]integrations.Question) (map[string]integrations.Answer, error) {
		asked = true
		return nil, nil
	}
	if picks, err := askCategories(context.Background(), none, payees, many); asked || picks != nil || err != nil {
		t.Fatal("more categories than one question can offer must skip the request")
	}
	if picks, err := askCategories(context.Background(), none, payees, nil); asked || picks != nil || err != nil {
		t.Fatal("without categories there is nothing to ask")
	}
}
