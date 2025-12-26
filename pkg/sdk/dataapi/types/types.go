package types

type SortBy string

const (
	SortByCURRENT    SortBy = "CURRENT"
	SortByINITIAL    SortBy = "INITIAL"
	SortByTOKENS     SortBy = "TOKENS" // default
	SortByCASHPNL    SortBy = "CASHPNL"
	SortByPERCENTPNL SortBy = "PERCENTPNL"
	SortByTITLE      SortBy = "TITLE"
	SortByRESOLVING  SortBy = "RESOLVING"
	SortByPRICE      SortBy = "PRICE"
	SortByAVGPRICE   SortBy = "AVGPRICE"
)

type SortDirection string

const (
	SortASC  SortDirection = "ASC"
	SortDESC SortDirection = "DESC" // default
)

type PositionsQuery struct {
	User          string         // required
	Market        []string       // conditionIds (0x+64hex), mutually exclusive with EventID
	EventID       []int64        // mutually exclusive with Market
	SizeThreshold *float64       // default: 1, >= 0
	Redeemable    *bool          // default: false
	Mergeable     *bool          // default: false
	Limit         *int           // default: 100, range: 0..500
	Offset        *int           // default: 0,   range: 0..10000
	SortBy        *SortBy        // default: TOKENS
	SortDirection *SortDirection // default: DESC
	Title         *string        // optional, max len 100
}

type Position struct {
	ProxyWallet        string  `json:"proxyWallet"` // 0x + 40 hex
	Asset              string  `json:"asset"`
	ConditionID        string  `json:"conditionId"` // 0x + 64 hex
	Size               float64 `json:"size"`
	AvgPrice           float64 `json:"avgPrice"`
	InitialValue       float64 `json:"initialValue"`
	CurrentValue       float64 `json:"currentValue"`
	CashPnl            float64 `json:"cashPnl"`
	PercentPnl         float64 `json:"percentPnl"`
	TotalBought        float64 `json:"totalBought"`
	RealizedPnl        float64 `json:"realizedPnl"`
	PercentRealizedPnl float64 `json:"percentRealizedPnl"`
	CurPrice           float64 `json:"curPrice"`
	Redeemable         bool    `json:"redeemable"`
	Mergeable          bool    `json:"mergeable"`
	Title              string  `json:"title"`
	Slug               string  `json:"slug"`
	Icon               string  `json:"icon"`
	EventSlug          string  `json:"eventSlug"`
	Outcome            string  `json:"outcome"`
	OutcomeIndex       int64   `json:"outcomeIndex"`
	OppositeOutcome    string  `json:"oppositeOutcome"`
	OppositeAsset      string  `json:"oppositeAsset"`
	EndDate            string  `json:"endDate"`
	NegativeRisk       bool    `json:"negativeRisk"`
}

type ActivityType string

const (
	ActivityTRADE      ActivityType = "TRADE"
	ActivitySPLIT      ActivityType = "SPLIT"
	ActivityMERGE      ActivityType = "MERGE"
	ActivityREDEEM     ActivityType = "REDEEM"
	ActivityREWARD     ActivityType = "REWARD"
	ActivityCONVERSION ActivityType = "CONVERSION"
)

type ActivitySortBy string

const (
	ActivitySortTIMESTAMP ActivitySortBy = "TIMESTAMP" // default
	ActivitySortTOKENS    ActivitySortBy = "TOKENS"
	ActivitySortCASH      ActivitySortBy = "CASH"
)

type Side string

const (
	SideBUY  Side = "BUY"
	SideSELL Side = "SELL"
)

type ActivityQuery struct {
	User          string          // required: 0x + 40 hex
	Limit         *int            // default 100, range 0..500
	Offset        *int            // default 0,   range 0..10000
	Market        []string        // conditionIds (0x + 64 hex), mutually exclusive with EventID
	EventID       []int64         // mutually exclusive with Market
	Type          []ActivityType  //
	Start         *int64          // >= 0 (unix seconds)
	End           *int64          // >= 0
	SortBy        *ActivitySortBy // default: TIMESTAMP
	SortDirection *SortDirection  // default: DESC
	Side          *Side           // BUY / SELL
}

type UserActivity struct {
	ProxyWallet           string  `json:"proxyWallet"`
	Timestamp             int64   `json:"timestamp"`
	ConditionID           string  `json:"conditionId"`
	Type                  string  `json:"type"` // TRADE/SPLIT/MERGE/REDEEM/REWARD/CONVERSION
	Size                  float64 `json:"size"`
	USDCSize              float64 `json:"usdcSize"`
	TransactionHash       string  `json:"transactionHash"`
	Price                 float64 `json:"price"`
	Asset                 string  `json:"asset"`
	Side                  string  `json:"side"` // BUY/SELL
	OutcomeIndex          int64   `json:"outcomeIndex"`
	Title                 string  `json:"title"`
	Slug                  string  `json:"slug"`
	Icon                  string  `json:"icon"`
	EventSlug             string  `json:"eventSlug"`
	Outcome               string  `json:"outcome"`
	Name                  string  `json:"name"`
	Pseudonym             string  `json:"pseudonym"`
	Bio                   string  `json:"bio"`
	ProfileImage          string  `json:"profileImage"`
	ProfileImageOptimized string  `json:"profileImageOptimized"`
}

type PositionValueQuery struct {
	User   string   // required
	Market []string // conditionIds (0x+64hex), mutually exclusive with EventID

}

type PositionValue struct {
	User  string  `json:"user"`
	Value float64 `json:"value"`
}
