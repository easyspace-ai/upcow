package types

import "github.com/shopspring/decimal"

type Event struct {
	ID                string           `json:"id"`
	Ticker            *string          `json:"ticker"`
	Slug              *string          `json:"slug"`
	Title             *string          `json:"title"`
	Subtitle          *string          `json:"subtitle"`
	Description       *string          `json:"description"`
	ResolutionSource  *string          `json:"resolutionSource"`
	StartDate         *string          `json:"startDate"`
	CreationDate      *string          `json:"creationDate"`
	EndDate           *string          `json:"endDate"`
	Image             *string          `json:"image"`
	Icon              *string          `json:"icon"`
	Active            *bool            `json:"active"`
	Closed            *bool            `json:"closed"`
	Archived          *bool            `json:"archived"`
	New               *bool            `json:"new"`
	Featured          *bool            `json:"featured"`
	Restricted        *bool            `json:"restricted"`
	Liquidity         *decimal.Decimal `json:"liquidity"`
	Volume            *decimal.Decimal `json:"volume"`
	OpenInterest      *decimal.Decimal `json:"openInterest"`
	SortBy            *string          `json:"sortBy"`
	Category          *string          `json:"category"`
	Subcategory       *string          `json:"subcategory"`
	IsTemplate        *bool            `json:"isTemplate"`
	TemplateVariables *string          `json:"templateVariables"`
	PublishedAt       *string          `json:"published_at"`
	CreatedBy         *string          `json:"createdBy"`
	UpdatedBy         *string          `json:"updatedBy"`
	CreatedAt         *string          `json:"createdAt"`
	UpdatedAt         *string          `json:"updatedAt"`
	CommentsEnabled   *bool            `json:"commentsEnabled"`
	Competitive       *decimal.Decimal `json:"competitive"` // number|null
	Volume24hr        *decimal.Decimal `json:"volume24hr"`
	Volume1wk         *decimal.Decimal `json:"volume1wk"`
	Volume1mo         *decimal.Decimal `json:"volume1mo"`
	Volume1yr         *decimal.Decimal `json:"volume1yr"`
	FeaturedImage     *string          `json:"featuredImage"`
	DisqusThread      *string          `json:"disqusThread"`
	ParentEvent       *string          `json:"parentEvent"`
	EnableOrderBook   *bool            `json:"enableOrderBook"`
	LiquidityAmm      *decimal.Decimal `json:"liquidityAmm"`
	LiquidityClob     *decimal.Decimal `json:"liquidityClob"`
	NegRisk           *bool            `json:"negRisk"`
	NegRiskMarketID   *string          `json:"negRiskMarketID"`
	NegRiskFeeBips    *int             `json:"negRiskFeeBips"`
	CommentCount      *int             `json:"commentCount"`

	ImageOptimized         *OptimizedImage `json:"imageOptimized"`
	IconOptimized          *OptimizedImage `json:"iconOptimized"`
	FeaturedImageOptimized *OptimizedImage `json:"featuredImageOptimized"`

	SubEvents []string `json:"subEvents"`
	Markets   []Market `json:"markets"`

	Series                []Series         `json:"series"`
	Categories            []Category       `json:"categories"`
	Collections           []Collection     `json:"collections"`
	Tags                  []Tag            `json:"tags"`
	Cyom                  *bool            `json:"cyom"`
	ClosedTime            *string          `json:"closedTime"`
	ShowAllOutcomes       *bool            `json:"showAllOutcomes"`
	ShowMarketImages      *bool            `json:"showMarketImages"`
	AutomaticallyResolved *bool            `json:"automaticallyResolved"`
	EnableNegRisk         *bool            `json:"enableNegRisk"`
	AutomaticallyActive   *bool            `json:"automaticallyActive"`
	EventDate             *string          `json:"eventDate"`
	StartTime             *string          `json:"startTime"`
	EventWeek             *int             `json:"eventWeek"`
	SeriesSlug            *string          `json:"seriesSlug"`
	Score                 *string          `json:"score"`
	Elapsed               *string          `json:"elapsed"`
	Period                *string          `json:"period"`
	Live                  *bool            `json:"live"`
	Ended                 *bool            `json:"ended"`
	FinishedTimestamp     *string          `json:"finishedTimestamp"`
	GmpChartMode          *string          `json:"gmpChartMode"`
	EventCreators         []EventCreator   `json:"eventCreators"`
	TweetCount            *int             `json:"tweetCount"`
	Chats                 []Chat           `json:"chats"`
	FeaturedOrder         *int             `json:"featuredOrder"`
	EstimateValue         *bool            `json:"estimateValue"`
	CantEstimate          *bool            `json:"cantEstimate"`
	EstimatedValue        *string          `json:"estimatedValue"`
	Templates             []Template       `json:"templates"`
	SpreadsMainLine       *decimal.Decimal `json:"spreadsMainLine"`
	TotalsMainLine        *decimal.Decimal `json:"totalsMainLine"`
	CarouselMap           *string          `json:"carouselMap"`
	PendingDeployment     *bool            `json:"pendingDeployment"`
	Deploying             *bool            `json:"deploying"`
	DeployingTimestamp    *string          `json:"deployingTimestamp"`
	ScheduledDeploymentTs *string          `json:"scheduledDeploymentTimestamp"`
	GameStatus            *string          `json:"gameStatus"`
}

type OptimizedImage struct {
	ID                        string           `json:"id"`
	ImageUrlSource            *string          `json:"imageUrlSource"`
	ImageUrlOptimized         *string          `json:"imageUrlOptimized"`
	ImageSizeKbSource         *decimal.Decimal `json:"imageSizeKbSource"`
	ImageSizeKbOptimized      *decimal.Decimal `json:"imageSizeKbOptimized"`
	ImageOptimizedComplete    *bool            `json:"imageOptimizedComplete"`
	ImageOptimizedLastUpdated *string          `json:"imageOptimizedLastUpdated"`
	RelID                     *int             `json:"relID"`
	Field                     *string          `json:"field"`
	Relname                   *string          `json:"relname"`
}

type Market struct {
	ID                    string           `json:"id"`
	Question              *string          `json:"question"`
	ConditionID           string           `json:"conditionId"`
	Slug                  *string          `json:"slug"`
	TwitterCardImage      *string          `json:"twitterCardImage"`
	ResolutionSource      *string          `json:"resolutionSource"`
	EndDate               *string          `json:"endDate"`
	Category              *string          `json:"category"`
	AmmType               *string          `json:"ammType"`
	Liquidity             *string          `json:"liquidity"` // 文档为 string|null
	SponsorName           *string          `json:"sponsorName"`
	SponsorImage          *string          `json:"sponsorImage"`
	StartDate             *string          `json:"startDate"`
	XAxisValue            *string          `json:"xAxisValue"`
	YAxisValue            *string          `json:"yAxisValue"`
	DenominationToken     *string          `json:"denominationToken"`
	Fee                   *string          `json:"fee"`
	Image                 *string          `json:"image"`
	Icon                  *string          `json:"icon"`
	LowerBound            *string          `json:"lowerBound"`
	UpperBound            *string          `json:"upperBound"`
	Description           *string          `json:"description"`
	Outcomes              *string          `json:"outcomes"`      // 多为字符串化数组
	OutcomePrices         *string          `json:"outcomePrices"` // 多为字符串化数组
	Volume                *string          `json:"volume"`        // 文档为 string|null
	Active                *bool            `json:"active"`
	MarketType            *string          `json:"marketType"`
	FormatType            *string          `json:"formatType"`
	LowerBoundDate        *string          `json:"lowerBoundDate"`
	UpperBoundDate        *string          `json:"upperBoundDate"`
	Closed                *bool            `json:"closed"`
	MarketMakerAddress    string           `json:"marketMakerAddress"`
	CreatedBy             *int             `json:"createdBy"`
	UpdatedBy             *int             `json:"updatedBy"`
	CreatedAt             *string          `json:"createdAt"`
	UpdatedAt             *string          `json:"updatedAt"`
	ClosedTime            *string          `json:"closedTime"`
	WideFormat            *bool            `json:"wideFormat"`
	New                   *bool            `json:"new"`
	MailchimpTag          *string          `json:"mailchimpTag"`
	Featured              *bool            `json:"featured"`
	Archived              *bool            `json:"archived"`
	ResolvedBy            *string          `json:"resolvedBy"`
	Restricted            *bool            `json:"restricted"`
	MarketGroup           *int             `json:"marketGroup"`
	GroupItemTitle        *string          `json:"groupItemTitle"`
	GroupItemThreshold    *string          `json:"groupItemThreshold"`
	QuestionID            *string          `json:"questionID"`
	UmaEndDate            *string          `json:"umaEndDate"`
	EnableOrderBook       *bool            `json:"enableOrderBook"`
	OrderPriceMinTickSize *decimal.Decimal `json:"orderPriceMinTickSize"` // number|null
	OrderMinSize          *decimal.Decimal `json:"orderMinSize"`          // number|null
	UmaResolutionStatus   *string          `json:"umaResolutionStatus"`
	CurationOrder         *int             `json:"curationOrder"`
	VolumeNum             *decimal.Decimal `json:"volumeNum"`
	LiquidityNum          *decimal.Decimal `json:"liquidityNum"`
	EndDateIso            *string          `json:"endDateIso"`
	StartDateIso          *string          `json:"startDateIso"`
	UmaEndDateIso         *string          `json:"umaEndDateIso"`
	HasReviewedDates      *bool            `json:"hasReviewedDates"`
	ReadyForCron          *bool            `json:"readyForCron"`
	CommentsEnabled       *bool            `json:"commentsEnabled"`
	Volume24hr            *decimal.Decimal `json:"volume24hr"`
	Volume1wk             *decimal.Decimal `json:"volume1wk"`
	Volume1mo             *decimal.Decimal `json:"volume1mo"`
	Volume1yr             *decimal.Decimal `json:"volume1yr"`
	GameStartTime         *string          `json:"gameStartTime"`
	SecondsDelay          *int             `json:"secondsDelay"`
	ClobTokenIds          *string          `json:"clobTokenIds"` // 多为字符串化数组
	DisqusThread          *string          `json:"disqusThread"`
	ShortOutcomes         *string          `json:"shortOutcomes"`
	TeamAID               *string          `json:"teamAID"`
	TeamBID               *string          `json:"teamBID"`
	UmaBond               *string          `json:"umaBond"`
	UmaReward             *string          `json:"umaReward"`
	FpmmLive              *bool            `json:"fpmmLive"`

	Volume24hrAmm  *decimal.Decimal `json:"volume24hrAmm"`
	Volume1wkAmm   *decimal.Decimal `json:"volume1wkAmm"`
	Volume1moAmm   *decimal.Decimal `json:"volume1moAmm"`
	Volume1yrAmm   *decimal.Decimal `json:"volume1yrAmm"`
	Volume24hrClob *decimal.Decimal `json:"volume24hrClob"`
	Volume1wkClob  *decimal.Decimal `json:"volume1wkClob"`
	Volume1moClob  *decimal.Decimal `json:"volume1moClob"`
	Volume1yrClob  *decimal.Decimal `json:"volume1yrClob"`
	VolumeAmm      *decimal.Decimal `json:"volumeAmm"`
	VolumeClob     *decimal.Decimal `json:"volumeClob"`
	LiquidityAmm   *decimal.Decimal `json:"liquidityAmm"`
	LiquidityClob  *decimal.Decimal `json:"liquidityClob"`

	Events []Event `json:"events"`

	ClobRewards []ClobRewards `json:"clobRewards"`

	MakerBaseFee         *int  `json:"makerBaseFee"`
	TakerBaseFee         *int  `json:"takerBaseFee"`
	CustomLiveness       *int  `json:"customLiveness"`
	AcceptingOrders      *bool `json:"acceptingOrders"`
	NotificationsEnabled *bool `json:"notificationsEnabled"`
	Score                *int  `json:"score"`

	ImageOptimized *OptimizedImage `json:"imageOptimized"`
}

type ClobRewards struct {
	Id           string `json:"id"`
	ConditionId  string `json:"conditionId"`
	AssetAddress string `json:"assetAddress"`
}

type Series struct {
	ID                *string            `json:"id"`
	Ticker            *string            `json:"ticker"`
	Slug              *string            `json:"slug"`
	Title             *string            `json:"title"`
	Subtitle          *string            `json:"subtitle"`
	SeriesType        *string            `json:"seriesType"`
	Recurrence        *string            `json:"recurrence"`
	Description       *string            `json:"description"`
	Image             *string            `json:"image"`
	Icon              *string            `json:"icon"`
	Layout            *string            `json:"layout"`
	Active            *bool              `json:"active"`
	Closed            *bool              `json:"closed"`
	Archived          *bool              `json:"archived"`
	New               *bool              `json:"new"`
	Featured          *bool              `json:"featured"`
	Restricted        *bool              `json:"restricted"`
	IsTemplate        *bool              `json:"isTemplate"`
	TemplateVariables *bool              `json:"templateVariables"`
	PublishedAt       *string            `json:"publishedAt"`
	CreatedBy         *string            `json:"createdBy"`
	UpdatedBy         *string            `json:"updatedBy"`
	CreatedAt         *string            `json:"createdAt"`
	UpdatedAt         *string            `json:"updatedAt"`
	CommentsEnabled   *bool              `json:"commentsEnabled"`
	Competitive       *decimal.Decimal   `json:"competitive"`
	Volume24hr        *decimal.Decimal   `json:"volume24hr"`
	Volume            *decimal.Decimal   `json:"volume"`
	Liquidity         *decimal.Decimal   `json:"liquidity"`
	StartDate         *string            `json:"startDate"`
	PythTokenID       *string            `json:"pythTokenID"`
	CgAssetName       *string            `json:"cgAssetName"`
	Score             *int               `json:"score"`
	Events            []map[string]any   `json:"events"`
	Collections       []SeriesCollection `json:"collections"`
	Categories        []Category         `json:"categories"`
	Tags              []Tag              `json:"tags"`
	CommentCount      *int               `json:"commentCount"`
	Chats             []Chat             `json:"chats"`
}

// Series.collections[] 的元素（与 Event.collections 的字段相似但命名不同）
type SeriesCollection struct {
	ID                   *string         `json:"id"`
	Ticker               *string         `json:"ticker"`
	Slug                 *string         `json:"slug"`
	Title                *string         `json:"title"`
	Subtitle             *string         `json:"subtitle"`
	CollectionType       *string         `json:"collectionType"`
	Description          *string         `json:"description"`
	Tags                 *string         `json:"tags"`
	Image                *string         `json:"image"`
	Icon                 *string         `json:"icon"`
	HeaderImage          *string         `json:"headerImage"`
	Layout               *string         `json:"layout"`
	Active               *bool           `json:"active"`
	Closed               *bool           `json:"closed"`
	Archived             *bool           `json:"archived"`
	New                  *bool           `json:"new"`
	Featured             *bool           `json:"featured"`
	Restricted           *bool           `json:"restricted"`
	IsTemplate           *bool           `json:"isTemplate"`
	TemplateVariables    *string         `json:"templateVariables"`
	PublishedAt          *string         `json:"publishedAt"`
	CreatedBy            *string         `json:"createdBy"`
	UpdatedBy            *string         `json:"updatedBy"`
	CreatedAt            *string         `json:"createdAt"`
	UpdatedAt            *string         `json:"updatedAt"`
	CommentsEnabled      *bool           `json:"commentsEnabled"`
	ImageOptimized       *OptimizedImage `json:"imageOptimized"`
	IconOptimized        *OptimizedImage `json:"iconOptimized"`
	HeaderImageOptimized *OptimizedImage `json:"headerImageOptimized"`
}

// Event.collections[]（与 SeriesCollection 基本一致）
type Collection struct {
	ID                   *string         `json:"id"`
	Ticker               *string         `json:"ticker"`
	Slug                 *string         `json:"slug"`
	Title                *string         `json:"title"`
	Subtitle             *string         `json:"subtitle"`
	CollectionType       *string         `json:"collectionType"`
	Description          *string         `json:"description"`
	Tags                 *string         `json:"tags"`
	Image                *string         `json:"image"`
	Icon                 *string         `json:"icon"`
	HeaderImage          *string         `json:"headerImage"`
	Layout               *string         `json:"layout"`
	Active               *bool           `json:"active"`
	Closed               *bool           `json:"closed"`
	Archived             *bool           `json:"archived"`
	New                  *bool           `json:"new"`
	Featured             *bool           `json:"featured"`
	Restricted           *bool           `json:"restricted"`
	IsTemplate           *bool           `json:"isTemplate"`
	TemplateVariables    *string         `json:"templateVariables"`
	PublishedAt          *string         `json:"publishedAt"`
	CreatedBy            *string         `json:"createdBy"`
	UpdatedBy            *string         `json:"updatedBy"`
	CreatedAt            *string         `json:"createdAt"`
	UpdatedAt            *string         `json:"updatedAt"`
	CommentsEnabled      *bool           `json:"commentsEnabled"`
	ImageOptimized       *OptimizedImage `json:"imageOptimized"`
	IconOptimized        *OptimizedImage `json:"iconOptimized"`
	HeaderImageOptimized *OptimizedImage `json:"headerImageOptimized"`
}

// 分类 & 标签
type Category struct {
	ID             *string `json:"id"`
	Label          *string `json:"label"`
	ParentCategory *string `json:"parentCategory"`
	Slug           *string `json:"slug"`
	PublishedAt    *string `json:"publishedAt"`
	CreatedBy      *string `json:"createdBy"`
	UpdatedBy      *string `json:"updatedBy"`
	CreatedAt      *string `json:"createdAt"`
	UpdatedAt      *string `json:"updatedAt"`
}

type Tag struct {
	ID          *string `json:"id"`
	Label       *string `json:"label"`
	Slug        *string `json:"slug"`
	ForceShow   *bool   `json:"forceShow"`
	PublishedAt *string `json:"publishedAt"`
	CreatedBy   *int    `json:"createdBy"`
	UpdatedBy   *int    `json:"updatedBy"`
	CreatedAt   *string `json:"createdAt"`
	UpdatedAt   *string `json:"updatedAt"`
	ForceHide   *bool   `json:"forceHide"`
	IsCarousel  *bool   `json:"isCarousel"`
}

type Chat struct {
	ID           *string `json:"id"`
	ChannelID    *string `json:"channelId"`
	ChannelName  *string `json:"channelName"`
	ChannelImage *string `json:"channelImage"`
	Live         *bool   `json:"live"`
	StartTime    *string `json:"startTime"`
	EndTime      *string `json:"endTime"`
}

type EventCreator struct {
	ID            *string `json:"id"`
	CreatorName   *string `json:"creatorName"`
	CreatorHandle *string `json:"creatorHandle"`
	CreatorURL    *string `json:"creatorUrl"`
	CreatorImage  *string `json:"creatorImage"`
	CreatedAt     *string `json:"createdAt"`
	UpdatedAt     *string `json:"updatedAt"`
}

type Template struct {
	ID               *string `json:"id"`
	EventTitle       *string `json:"eventTitle"`
	EventSlug        *string `json:"eventSlug"`
	EventImage       *string `json:"eventImage"`
	MarketTitle      *string `json:"marketTitle"`
	Description      *string `json:"description"`
	ResolutionSource *string `json:"resolutionSource"`
	NegRisk          *bool   `json:"negRisk"`
	SortBy           *string `json:"sortBy"`
	ShowMarketImages *bool   `json:"showMarketImages"`
	SeriesSlug       *string `json:"seriesSlug"`
	Outcomes         *string `json:"outcomes"`
}
