package dataapi

import (
	"fmt"
	"github.com/betbot/gobet/pkg/sdk/dataapi/types"
	http2 "github.com/betbot/gobet/pkg/sdk/http"
	"math/big"
	"net/http"
	"regexp"
	"strings"
)

type Client struct {
	client *http2.Client

	chainId *big.Int
}

func NewClient(host string, chainId *big.Int) *Client {
	if strings.HasSuffix(host, "/") {
		host = host[:len(host)-1]
	}
	return &Client{
		client:  http2.NewClient(host),
		chainId: chainId,
	}
}

func (c *Client) GetPositions(q types.PositionsQuery) ([]types.Position, error) {
	if strings.TrimSpace(q.User) == "" {
		return nil, fmt.Errorf("user is required")
	}
	if len(q.Market) > 0 && len(q.EventID) > 0 {
		return nil, fmt.Errorf("market and eventId are mutually exclusive")
	}
	if len(q.Market) > 0 {
		re := regexp.MustCompile(`^0x[0-9a-fA-F]{64}$`)
		for _, m := range q.Market {
			if !re.MatchString(m) {
				return nil, fmt.Errorf("invalid conditionId: %s (must be 0x + 64 hex chars)", m)
			}
		}
	}

	limit := 100
	if q.Limit != nil {
		if *q.Limit < 0 || *q.Limit > 500 {
			return nil, fmt.Errorf("limit out of range (0..500)")
		}
		limit = *q.Limit
	}
	offset := 0
	if q.Offset != nil {
		if *q.Offset < 0 || *q.Offset > 10000 {
			return nil, fmt.Errorf("offset out of range (0..10000)")
		}
		offset = *q.Offset
	}
	sortBy := types.SortByTOKENS
	if q.SortBy != nil {
		sortBy = *q.SortBy
	}
	sortDir := types.SortDESC
	if q.SortDirection != nil {
		sortDir = *q.SortDirection
	}
	if q.Title != nil && len(*q.Title) > 100 {
		return nil, fmt.Errorf("title too long (max 100)")
	}

	params := map[string]any{
		"user":          q.User,
		"limit":         limit,
		"offset":        offset,
		"sortBy":        string(sortBy),
		"sortDirection": string(sortDir),
	}
	if q.SizeThreshold != nil {
		if *q.SizeThreshold < 0 {
			return nil, fmt.Errorf("sizeThreshold must be >= 0")
		}
		params["sizeThreshold"] = *q.SizeThreshold
	}
	if q.Redeemable != nil {
		params["redeemable"] = *q.Redeemable
	}
	if q.Mergeable != nil {
		params["mergeable"] = *q.Mergeable
	}
	if q.Title != nil && *q.Title != "" {
		params["title"] = *q.Title
	}
	if len(q.Market) > 0 {
		params["market"] = strings.Join(q.Market, ",")
	}
	if len(q.EventID) > 0 {
		strIDs := make([]string, 0, len(q.EventID))
		for _, id := range q.EventID {
			strIDs = append(strIDs, fmt.Sprintf("%d", id))
		}
		params["eventId"] = strings.Join(strIDs, ",")
	}

	var out []types.Position
	resp, err := c.client.DoRequest(http.MethodGet, types.GET_POSITIONS, &http2.RequestOptions{
		Params: params,
	}, &out)
	if _, e := http2.ParseHTTPError(resp, err); e != nil {
		return nil, e
	}
	return out, nil
}

func (c *Client) GetUserActivity(q types.ActivityQuery) ([]types.UserActivity, error) {
	if strings.TrimSpace(q.User) == "" {
		return nil, fmt.Errorf("user is required")
	}
	if len(q.Market) > 0 && len(q.EventID) > 0 {
		return nil, fmt.Errorf("market and eventId are mutually exclusive")
	}

	if len(q.Market) > 0 {
		re := regexp.MustCompile(`^0x[0-9a-fA-F]{64}$`)
		for _, m := range q.Market {
			if !re.MatchString(m) {
				return nil, fmt.Errorf("invalid conditionId: %s (must be 0x + 64 hex)", m)
			}
		}
	}
	limit := 100
	if q.Limit != nil {
		if *q.Limit < 0 || *q.Limit > 500 {
			return nil, fmt.Errorf("limit out of range (0..500)")
		}
		limit = *q.Limit
	}
	offset := 0
	if q.Offset != nil {
		if *q.Offset < 0 || *q.Offset > 10000 {
			return nil, fmt.Errorf("offset out of range (0..10000)")
		}
		offset = *q.Offset
	}
	sortBy := types.ActivitySortTIMESTAMP
	if q.SortBy != nil {
		sortBy = *q.SortBy
	}
	sortDir := types.SortDESC
	if q.SortDirection != nil {
		sortDir = *q.SortDirection
	}
	if q.Start != nil && *q.Start < 0 {
		return nil, fmt.Errorf("start must be >= 0")
	}
	if q.End != nil && *q.End < 0 {
		return nil, fmt.Errorf("end must be >= 0")
	}

	// -- query params --
	params := map[string]any{
		"user":          q.User,
		"limit":         limit,
		"offset":        offset,
		"sortBy":        string(sortBy),
		"sortDirection": string(sortDir),
	}
	if len(q.Market) > 0 {
		params["market"] = strings.Join(q.Market, ",")
	}
	if len(q.EventID) > 0 {
		strIDs := make([]string, 0, len(q.EventID))
		for _, id := range q.EventID {
			strIDs = append(strIDs, fmt.Sprintf("%d", id))
		}
		params["eventId"] = strings.Join(strIDs, ",")
	}
	if len(q.Type) > 0 {
		typesStr := make([]string, 0, len(q.Type))
		for _, t := range q.Type {
			typesStr = append(typesStr, string(t))
		}
		params["type"] = strings.Join(typesStr, ",")
	}
	if q.Start != nil {
		params["start"] = *q.Start
	}
	if q.End != nil {
		params["end"] = *q.End
	}
	if q.Side != nil {
		params["side"] = string(*q.Side)
	}

	var out []types.UserActivity
	resp, err := c.client.DoRequest(http.MethodGet, types.GET_Activity, &http2.RequestOptions{
		Params: params,
	}, &out)
	if _, e := http2.ParseHTTPError(resp, err); e != nil {
		return nil, e
	}
	return out, nil
}

func (c *Client) GetPositionValue(q types.PositionValueQuery) ([]types.PositionValue, error) {
	if strings.TrimSpace(q.User) == "" {
		return nil, fmt.Errorf("user is required")
	}
	params := map[string]any{
		"user": q.User,
	}
	if len(q.Market) > 0 {
		params["market"] = strings.Join(q.Market, ",")
	}

	var out []types.PositionValue
	resp, err := c.client.DoRequest(http.MethodGet, types.GET_VALUE, &http2.RequestOptions{
		Params: params,
	}, &out)
	if _, e := http2.ParseHTTPError(resp, err); e != nil {
		return nil, e
	}
	return out, nil

}
