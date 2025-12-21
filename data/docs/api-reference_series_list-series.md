# List series - Polymarket Documentation

Skip to main content
Polymarket Documentation
 home page
Search...
⌘
K
Main Site
Main Site
Search...
Navigation
Series
List series
User Guide
For Developers
Changelog
Polymarket
Discord Community
Twitter
Developer Quickstart
Developer Quickstart
Your First Order
Glossary
API Rate Limits
Endpoints
Polymarket Builders Program
Builder Program Introduction
Builder Profile & Keys
Order Attribution
Relayer Client
Examples
Central Limit Order Book
CLOB Introduction
Status
Quickstart
Authentication
Client
REST API
Historical Timeseries Data
Order Management
Trades
Websocket
WSS Overview
WSS Quickstart
WSS Authentication
User Channel
Market Channel
Real Time Data Stream
RTDS Overview
RTDS Crypto Prices
RTDS Comments
Gamma Structure
Overview
Gamma Structure
Fetching Markets
Gamma Endpoints
Health
Sports
Tags
Events
Markets
Series
GET
List series
GET
Get series by id
Comments
Search
Data-API
Health
Core
Misc
Builders
Bridge & Swap
Overview
POST
Create Deposit
GET
Get Supported Assets
Subgraph
Overview
Resolution
Resolution
Rewards
Liquidity Rewards
Conditional Token Frameworks
Overview
Splitting USDC
Merging Tokens
Reedeeming Tokens
Deployment and Additional Information
Proxy Wallets
Proxy wallet
Negative Risk
Overview
List series
cURL
Copy
Ask AI
curl
 --request
 GET
 \


  --url
 https://gamma-api.polymarket.com/series
200
Copy
Ask AI
[


  {


    "id"
: 
"<string>"
,


    "ticker"
: 
"<string>"
,


    "slug"
: 
"<string>"
,


    "title"
: 
"<string>"
,


    "subtitle"
: 
"<string>"
,


    "seriesType"
: 
"<string>"
,


    "recurrence"
: 
"<string>"
,


    "description"
: 
"<string>"
,


    "image"
: 
"<string>"
,


    "icon"
: 
"<string>"
,


    "layout"
: 
"<string>"
,


    "active"
: 
true
,


    "closed"
: 
true
,


    "archived"
: 
true
,


    "new"
: 
true
,


    "featured"
: 
true
,


    "restricted"
: 
true
,


    "isTemplate"
: 
true
,


    "templateVariables"
: 
true
,


    "publishedAt"
: 
"<string>"
,


    "createdBy"
: 
"<string>"
,


    "updatedBy"
: 
"<string>"
,


    "createdAt"
: 
"2023-11-07T05:31:56Z"
,


    "updatedAt"
: 
"2023-11-07T05:31:56Z"
,


    "commentsEnabled"
: 
true
,


    "competitive"
: 
"<string>"
,


    "volume24hr"
: 
123
,


    "volume"
: 
123
,


    "liquidity"
: 
123
,


    "startDate"
: 
"2023-11-07T05:31:56Z"
,


    "pythTokenID"
: 
"<string>"
,


    "cgAssetName"
: 
"<string>"
,


    "score"
: 
123
,


    "events"
: [


      {


        "id"
: 
"<string>"
,


        "ticker"
: 
"<string>"
,


        "slug"
: 
"<string>"
,


        "title"
: 
"<string>"
,


        "subtitle"
: 
"<string>"
,


        "description"
: 
"<string>"
,


        "resolutionSource"
: 
"<string>"
,


        "startDate"
: 
"2023-11-07T05:31:56Z"
,


        "creationDate"
: 
"2023-11-07T05:31:56Z"
,


        "endDate"
: 
"2023-11-07T05:31:56Z"
,


        "image"
: 
"<string>"
,


        "icon"
: 
"<string>"
,


        "active"
: 
true
,


        "closed"
: 
true
,


        "archived"
: 
true
,


        "new"
: 
true
,


        "featured"
: 
true
,


        "restricted"
: 
true
,


        "liquidity"
: 
123
,


        "volume"
: 
123
,


        "openInterest"
: 
123
,


        "sortBy"
: 
"<string>"
,


        "category"
: 
"<string>"
,


        "subcategory"
: 
"<string>"
,


        "isTemplate"
: 
true
,


        "templateVariables"
: 
"<string>"
,


        "published_at"
: 
"<string>"
,


        "createdBy"
: 
"<string>"
,


        "updatedBy"
: 
"<string>"
,


        "createdAt"
: 
"2023-11-07T05:31:56Z"
,


        "updatedAt"
: 
"2023-11-07T05:31:56Z"
,


        "commentsEnabled"
: 
true
,


        "competitive"
: 
123
,


        "volume24hr"
: 
123
,


        "volume1wk"
: 
123
,


        "volume1mo"
: 
123
,


        "volume1yr"
: 
123
,


        "featuredImage"
: 
"<string>"
,


        "disqusThread"
: 
"<string>"
,


        "parentEvent"
: 
"<string>"
,


        "enableOrderBook"
: 
true
,


        "liquidityAmm"
: 
123
,


        "liquidityClob"
: 
123
,


        "negRisk"
: 
true
,


        "negRiskMarketID"
: 
"<string>"
,


        "negRiskFeeBips"
: 
123
,


        "commentCount"
: 
123
,


        "imageOptimized"
: {


          "id"
: 
"<string>"
,


          "imageUrlSource"
: 
"<string>"
,


          "imageUrlOptimized"
: 
"<string>"
,


          "imageSizeKbSource"
: 
123
,


          "imageSizeKbOptimized"
: 
123
,


          "imageOptimizedComplete"
: 
true
,


          "imageOptimizedLastUpdated"
: 
"<string>"
,


          "relID"
: 
123
,


          "field"
: 
"<string>"
,


          "relname"
: 
"<string>"


        },


        "iconOptimized"
: {


          "id"
: 
"<string>"
,


          "imageUrlSource"
: 
"<string>"
,


          "imageUrlOptimized"
: 
"<string>"
,


          "imageSizeKbSource"
: 
123
,


          "imageSizeKbOptimized"
: 
123
,


          "imageOptimizedComplete"
: 
true
,


          "imageOptimizedLastUpdated"
: 
"<string>"
,


          "relID"
: 
123
,


          "field"
: 
"<string>"
,


          "relname"
: 
"<string>"


        },


        "featuredImageOptimized"
: {


          "id"
: 
"<string>"
,


          "imageUrlSource"
: 
"<string>"
,


          "imageUrlOptimized"
: 
"<string>"
,


          "imageSizeKbSource"
: 
123
,


          "imageSizeKbOptimized"
: 
123
,


          "imageOptimizedComplete"
: 
true
,


          "imageOptimizedLastUpdated"
: 
"<string>"
,


          "relID"
: 
123
,


          "field"
: 
"<string>"
,


          "relname"
: 
"<string>"


        },


        "subEvents"
: [


          "<string>"


        ],


        "markets"
: [


          {


            "id"
: 
"<string>"
,


            "question"
: 
"<string>"
,


            "conditionId"
: 
"<string>"
,


            "slug"
: 
"<string>"
,


            "twitterCardImage"
: 
"<string>"
,


            "resolutionSource"
: 
"<string>"
,


            "endDate"
: 
"2023-11-07T05:31:56Z"
,


            "category"
: 
"<string>"
,


            "ammType"
: 
"<string>"
,


            "liquidity"
: 
"<string>"
,


            "sponsorName"
: 
"<string>"
,


            "sponsorImage"
: 
"<string>"
,


            "startDate"
: 
"2023-11-07T05:31:56Z"
,


            "xAxisValue"
: 
"<string>"
,


            "yAxisValue"
: 
"<string>"
,


            "denominationToken"
: 
"<string>"
,


            "fee"
: 
"<string>"
,


            "image"
: 
"<string>"
,


            "icon"
: 
"<string>"
,


            "lowerBound"
: 
"<string>"
,


            "upperBound"
: 
"<string>"
,


            "description"
: 
"<string>"
,


            "outcomes"
: 
"<string>"
,


            "outcomePrices"
: 
"<string>"
,


            "volume"
: 
"<string>"
,


            "active"
: 
true
,


            "marketType"
: 
"<string>"
,


            "formatType"
: 
"<string>"
,


            "lowerBoundDate"
: 
"<string>"
,


            "upperBoundDate"
: 
"<string>"
,


            "closed"
: 
true
,


            "marketMakerAddress"
: 
"<string>"
,


            "createdBy"
: 
123
,


            "updatedBy"
: 
123
,


            "createdAt"
: 
"2023-11-07T05:31:56Z"
,


            "updatedAt"
: 
"2023-11-07T05:31:56Z"
,


            "closedTime"
: 
"<string>"
,


            "wideFormat"
: 
true
,


            "new"
: 
true
,


            "mailchimpTag"
: 
"<string>"
,


            "featured"
: 
true
,


            "archived"
: 
true
,


            "resolvedBy"
: 
"<string>"
,


            "restricted"
: 
true
,


            "marketGroup"
: 
123
,


            "groupItemTitle"
: 
"<string>"
,


            "groupItemThreshold"
: 
"<string>"
,


            "questionID"
: 
"<string>"
,


            "umaEndDate"
: 
"<string>"
,


            "enableOrderBook"
: 
true
,


            "orderPriceMinTickSize"
: 
123
,


            "orderMinSize"
: 
123
,


            "umaResolutionStatus"
: 
"<string>"
,


            "curationOrder"
: 
123
,


            "volumeNum"
: 
123
,


            "liquidityNum"
: 
123
,


            "endDateIso"
: 
"<string>"
,


            "startDateIso"
: 
"<string>"
,


            "umaEndDateIso"
: 
"<string>"
,


            "hasReviewedDates"
: 
true
,


            "readyForCron"
: 
true
,


            "commentsEnabled"
: 
true
,


            "volume24hr"
: 
123
,


            "volume1wk"
: 
123
,


            "volume1mo"
: 
123
,


            "volume1yr"
: 
123
,


            "gameStartTime"
: 
"<string>"
,


            "secondsDelay"
: 
123
,


            "clobTokenIds"
: 
"<string>"
,


            "disqusThread"
: 
"<string>"
,


            "shortOutcomes"
: 
"<string>"
,


            "teamAID"
: 
"<string>"
,


            "teamBID"
: 
"<string>"
,


            "umaBond"
: 
"<string>"
,


            "umaReward"
: 
"<string>"
,


            "fpmmLive"
: 
true
,


            "volume24hrAmm"
: 
123
,


            "volume1wkAmm"
: 
123
,


            "volume1moAmm"
: 
123
,


            "volume1yrAmm"
: 
123
,


            "volume24hrClob"
: 
123
,


            "volume1wkClob"
: 
123
,


            "volume1moClob"
: 
123
,


            "volume1yrClob"
: 
123
,


            "volumeAmm"
: 
123
,


            "volumeClob"
: 
123
,


            "liquidityAmm"
: 
123
,


            "liquidityClob"
: 
123
,


            "makerBaseFee"
: 
123
,


            "takerBaseFee"
: 
123
,


            "customLiveness"
: 
123
,


            "acceptingOrders"
: 
true
,


            "notificationsEnabled"
: 
true
,


            "score"
: 
123
,


            "imageOptimized"
: {


              "id"
: 
"<string>"
,


              "imageUrlSource"
: 
"<string>"
,


              "imageUrlOptimized"
: 
"<string>"
,


              "imageSizeKbSource"
: 
123
,


              "imageSizeKbOptimized"
: 
123
,


              "imageOptimizedComplete"
: 
true
,


              "imageOptimizedLastUpdated"
: 
"<string>"
,


              "relID"
: 
123
,


              "field"
: 
"<string>"
,


              "relname"
: 
"<string>"


            },


            "iconOptimized"
: {


              "id"
: 
"<string>"
,


              "imageUrlSource"
: 
"<string>"
,


              "imageUrlOptimized"
: 
"<string>"
,


              "imageSizeKbSource"
: 
123
,


              "imageSizeKbOptimized"
: 
123
,


              "imageOptimizedComplete"
: 
true
,


              "imageOptimizedLastUpdated"
: 
"<string>"
,


              "relID"
: 
123
,


              "field"
: 
"<string>"
,


              "relname"
: 
"<string>"


            },


            "events"
: 
"<array>"
,


            "categories"
: [


              {


                "id"
: 
"<string>"
,


                "label"
: 
"<string>"
,


                "parentCategory"
: 
"<string>"
,


                "slug"
: 
"<string>"
,


                "publishedAt"
: 
"<string>"
,


                "createdBy"
: 
"<string>"
,


                "updatedBy"
: 
"<string>"
,


                "createdAt"
: 
"2023-11-07T05:31:56Z"
,


                "updatedAt"
: 
"2023-11-07T05:31:56Z"


              }


            ],


            "tags"
: [


              {


                "id"
: 
"<string>"
,


                "label"
: 
"<string>"
,


                "slug"
: 
"<string>"
,


                "forceShow"
: 
true
,


                "publishedAt"
: 
"<string>"
,


                "createdBy"
: 
123
,


                "updatedBy"
: 
123
,


                "createdAt"
: 
"2023-11-07T05:31:56Z"
,


                "updatedAt"
: 
"2023-11-07T05:31:56Z"
,


                "forceHide"
: 
true
,


                "isCarousel"
: 
true


              }


            ],


            "creator"
: 
"<string>"
,


            "ready"
: 
true
,


            "funded"
: 
true
,


            "pastSlugs"
: 
"<string>"
,


            "readyTimestamp"
: 
"2023-11-07T05:31:56Z"
,


            "fundedTimestamp"
: 
"2023-11-07T05:31:56Z"
,


            "acceptingOrdersTimestamp"
: 
"2023-11-07T05:31:56Z"
,


            "competitive"
: 
123
,


            "rewardsMinSize"
: 
123
,


            "rewardsMaxSpread"
: 
123
,


            "spread"
: 
123
,


            "automaticallyResolved"
: 
true
,


            "oneDayPriceChange"
: 
123
,


            "oneHourPriceChange"
: 
123
,


            "oneWeekPriceChange"
: 
123
,


            "oneMonthPriceChange"
: 
123
,


            "oneYearPriceChange"
: 
123
,


            "lastTradePrice"
: 
123
,


            "bestBid"
: 
123
,


            "bestAsk"
: 
123
,


            "automaticallyActive"
: 
true
,


            "clearBookOnStart"
: 
true
,


            "chartColor"
: 
"<string>"
,


            "seriesColor"
: 
"<string>"
,


            "showGmpSeries"
: 
true
,


            "showGmpOutcome"
: 
true
,


            "manualActivation"
: 
true
,


            "negRiskOther"
: 
true
,


            "gameId"
: 
"<string>"
,


            "groupItemRange"
: 
"<string>"
,


            "sportsMarketType"
: 
"<string>"
,


            "line"
: 
123
,


            "umaResolutionStatuses"
: 
"<string>"
,


            "pendingDeployment"
: 
true
,


            "deploying"
: 
true
,


            "deployingTimestamp"
: 
"2023-11-07T05:31:56Z"
,


            "scheduledDeploymentTimestamp"
: 
"2023-11-07T05:31:56Z"
,


            "rfqEnabled"
: 
true
,


            "eventStartTime"
: 
"2023-11-07T05:31:56Z"


          }


        ],


        "series"
: 
"<array>"
,


        "categories"
: [


          {


            "id"
: 
"<string>"
,


            "label"
: 
"<string>"
,


            "parentCategory"
: 
"<string>"
,


            "slug"
: 
"<string>"
,


            "publishedAt"
: 
"<string>"
,


            "createdBy"
: 
"<string>"
,


            "updatedBy"
: 
"<string>"
,


            "createdAt"
: 
"2023-11-07T05:31:56Z"
,


            "updatedAt"
: 
"2023-11-07T05:31:56Z"


          }


        ],


        "collections"
: [


          {


            "id"
: 
"<string>"
,


            "ticker"
: 
"<string>"
,


            "slug"
: 
"<string>"
,


            "title"
: 
"<string>"
,


            "subtitle"
: 
"<string>"
,


            "collectionType"
: 
"<string>"
,


            "description"
: 
"<string>"
,


            "tags"
: 
"<string>"
,


            "image"
: 
"<string>"
,


            "icon"
: 
"<string>"
,


            "headerImage"
: 
"<string>"
,


            "layout"
: 
"<string>"
,


            "active"
: 
true
,


            "closed"
: 
true
,


            "archived"
: 
true
,


            "new"
: 
true
,


            "featured"
: 
true
,


            "restricted"
: 
true
,


            "isTemplate"
: 
true
,


            "templateVariables"
: 
"<string>"
,


            "publishedAt"
: 
"<string>"
,


            "createdBy"
: 
"<string>"
,


            "updatedBy"
: 
"<string>"
,


            "createdAt"
: 
"2023-11-07T05:31:56Z"
,


            "updatedAt"
: 
"2023-11-07T05:31:56Z"
,


            "commentsEnabled"
: 
true
,


            "imageOptimized"
: {


              "id"
: 
"<string>"
,


              "imageUrlSource"
: 
"<string>"
,


              "imageUrlOptimized"
: 
"<string>"
,


              "imageSizeKbSource"
: 
123
,


              "imageSizeKbOptimized"
: 
123
,


              "imageOptimizedComplete"
: 
true
,


              "imageOptimizedLastUpdated"
: 
"<string>"
,


              "relID"
: 
123
,


              "field"
: 
"<string>"
,


              "relname"
: 
"<string>"


            },


            "iconOptimized"
: {


              "id"
: 
"<string>"
,


              "imageUrlSource"
: 
"<string>"
,


              "imageUrlOptimized"
: 
"<string>"
,


              "imageSizeKbSource"
: 
123
,


              "imageSizeKbOptimized"
: 
123
,


              "imageOptimizedComplete"
: 
true
,


              "imageOptimizedLastUpdated"
: 
"<string>"
,


              "relID"
: 
123
,


              "field"
: 
"<string>"
,


              "relname"
: 
"<string>"


            },


            "headerImageOptimized"
: {


              "id"
: 
"<string>"
,


              "imageUrlSource"
: 
"<string>"
,


              "imageUrlOptimized"
: 
"<string>"
,


              "imageSizeKbSource"
: 
123
,


              "imageSizeKbOptimized"
: 
123
,


              "imageOptimizedComplete"
: 
true
,


              "imageOptimizedLastUpdated"
: 
"<string>"
,


              "relID"
: 
123
,


              "field"
: 
"<string>"
,


              "relname"
: 
"<string>"


            }


          }


        ],


        "tags"
: [


          {


            "id"
: 
"<string>"
,


            "label"
: 
"<string>"
,


            "slug"
: 
"<string>"
,


            "forceShow"
: 
true
,


            "publishedAt"
: 
"<string>"
,


            "createdBy"
: 
123
,


            "updatedBy"
: 
123
,


            "createdAt"
: 
"2023-11-07T05:31:56Z"
,


            "updatedAt"
: 
"2023-11-07T05:31:56Z"
,


            "forceHide"
: 
true
,


            "isCarousel"
: 
true


          }


        ],


        "cyom"
: 
true
,


        "closedTime"
: 
"2023-11-07T05:31:56Z"
,


        "showAllOutcomes"
: 
true
,


        "showMarketImages"
: 
true
,


        "automaticallyResolved"
: 
true
,


        "enableNegRisk"
: 
true
,


        "automaticallyActive"
: 
true
,


        "eventDate"
: 
"<string>"
,


        "startTime"
: 
"2023-11-07T05:31:56Z"
,


        "eventWeek"
: 
123
,


        "seriesSlug"
: 
"<string>"
,


        "score"
: 
"<string>"
,


        "elapsed"
: 
"<string>"
,


        "period"
: 
"<string>"
,


        "live"
: 
true
,


        "ended"
: 
true
,


        "finishedTimestamp"
: 
"2023-11-07T05:31:56Z"
,


        "gmpChartMode"
: 
"<string>"
,


        "eventCreators"
: [


          {


            "id"
: 
"<string>"
,


            "creatorName"
: 
"<string>"
,


            "creatorHandle"
: 
"<string>"
,


            "creatorUrl"
: 
"<string>"
,


            "creatorImage"
: 
"<string>"
,


            "createdAt"
: 
"2023-11-07T05:31:56Z"
,


            "updatedAt"
: 
"2023-11-07T05:31:56Z"


          }


        ],


        "tweetCount"
: 
123
,


        "chats"
: [


          {


            "id"
: 
"<string>"
,


            "channelId"
: 
"<string>"
,


            "channelName"
: 
"<string>"
,


            "channelImage"
: 
"<string>"
,


            "live"
: 
true
,


            "startTime"
: 
"2023-11-07T05:31:56Z"
,


            "endTime"
: 
"2023-11-07T05:31:56Z"


          }


        ],


        "featuredOrder"
: 
123
,


        "estimateValue"
: 
true
,


        "cantEstimate"
: 
true
,


        "estimatedValue"
: 
"<string>"
,


        "templates"
: [


          {


            "id"
: 
"<string>"
,


            "eventTitle"
: 
"<string>"
,


            "eventSlug"
: 
"<string>"
,


            "eventImage"
: 
"<string>"
,


            "marketTitle"
: 
"<string>"
,


            "description"
: 
"<string>"
,


            "resolutionSource"
: 
"<string>"
,


            "negRisk"
: 
true
,


            "sortBy"
: 
"<string>"
,


            "showMarketImages"
: 
true
,


            "seriesSlug"
: 
"<string>"
,


            "outcomes"
: 
"<string>"


          }


        ],


        "spreadsMainLine"
: 
123
,


        "totalsMainLine"
: 
123
,


        "carouselMap"
: 
"<string>"
,


        "pendingDeployment"
: 
true
,


        "deploying"
: 
true
,


        "deployingTimestamp"
: 
"2023-11-07T05:31:56Z"
,


        "scheduledDeploymentTimestamp"
: 
"2023-11-07T05:31:56Z"
,


        "gameStatus"
: 
"<string>"


      }


    ],


    "collections"
: [


      {


        "id"
: 
"<string>"
,


        "ticker"
: 
"<string>"
,


        "slug"
: 
"<string>"
,


        "title"
: 
"<string>"
,


        "subtitle"
: 
"<string>"
,


        "collectionType"
: 
"<string>"
,


        "description"
: 
"<string>"
,


        "tags"
: 
"<string>"
,


        "image"
: 
"<string>"
,


        "icon"
: 
"<string>"
,


        "headerImage"
: 
"<string>"
,


        "layout"
: 
"<string>"
,


        "active"
: 
true
,


        "closed"
: 
true
,


        "archived"
: 
true
,


        "new"
: 
true
,


        "featured"
: 
true
,


        "restricted"
: 
true
,


        "isTemplate"
: 
true
,


        "templateVariables"
: 
"<string>"
,


        "publishedAt"
: 
"<string>"
,


        "createdBy"
: 
"<string>"
,


        "updatedBy"
: 
"<string>"
,


        "createdAt"
: 
"2023-11-07T05:31:56Z"
,


        "updatedAt"
: 
"2023-11-07T05:31:56Z"
,


        "commentsEnabled"
: 
true
,


        "imageOptimized"
: {


          "id"
: 
"<string>"
,


          "imageUrlSource"
: 
"<string>"
,


          "imageUrlOptimized"
: 
"<string>"
,


          "imageSizeKbSource"
: 
123
,


          "imageSizeKbOptimized"
: 
123
,


          "imageOptimizedComplete"
: 
true
,


          "imageOptimizedLastUpdated"
: 
"<string>"
,


          "relID"
: 
123
,


          "field"
: 
"<string>"
,


          "relname"
: 
"<string>"


        },


        "iconOptimized"
: {


          "id"
: 
"<string>"
,


          "imageUrlSource"
: 
"<string>"
,


          "imageUrlOptimized"
: 
"<string>"
,


          "imageSizeKbSource"
: 
123
,


          "imageSizeKbOptimized"
: 
123
,


          "imageOptimizedComplete"
: 
true
,


          "imageOptimizedLastUpdated"
: 
"<string>"
,


          "relID"
: 
123
,


          "field"
: 
"<string>"
,


          "relname"
: 
"<string>"


        },


        "headerImageOptimized"
: {


          "id"
: 
"<string>"
,


          "imageUrlSource"
: 
"<string>"
,


          "imageUrlOptimized"
: 
"<string>"
,


          "imageSizeKbSource"
: 
123
,


          "imageSizeKbOptimized"
: 
123
,


          "imageOptimizedComplete"
: 
true
,


          "imageOptimizedLastUpdated"
: 
"<string>"
,


          "relID"
: 
123
,


          "field"
: 
"<string>"
,


          "relname"
: 
"<string>"


        }


      }


    ],


    "categories"
: [


      {


        "id"
: 
"<string>"
,


        "label"
: 
"<string>"
,


        "parentCategory"
: 
"<string>"
,


        "slug"
: 
"<string>"
,


        "publishedAt"
: 
"<string>"
,


        "createdBy"
: 
"<string>"
,


        "updatedBy"
: 
"<string>"
,


        "createdAt"
: 
"2023-11-07T05:31:56Z"
,


        "updatedAt"
: 
"2023-11-07T05:31:56Z"


      }


    ],


    "tags"
: [


      {


        "id"
: 
"<string>"
,


        "label"
: 
"<string>"
,


        "slug"
: 
"<string>"
,


        "forceShow"
: 
true
,


        "publishedAt"
: 
"<string>"
,


        "createdBy"
: 
123
,


        "updatedBy"
: 
123
,


        "createdAt"
: 
"2023-11-07T05:31:56Z"
,


        "updatedAt"
: 
"2023-11-07T05:31:56Z"
,


        "forceHide"
: 
true
,


        "isCarousel"
: 
true


      }


    ],


    "commentCount"
: 
123
,


    "chats"
: [


      {


        "id"
: 
"<string>"
,


        "channelId"
: 
"<string>"
,


        "channelName"
: 
"<string>"
,


        "channelImage"
: 
"<string>"
,


        "live"
: 
true
,


        "startTime"
: 
"2023-11-07T05:31:56Z"
,


        "endTime"
: 
"2023-11-07T05:31:56Z"


      }


    ]


  }


]
Series
List series
GET
/
series
Try it
List series
cURL
Copy
Ask AI
curl
 --request
 GET
 \


  --url
 https://gamma-api.polymarket.com/series
200
Copy
Ask AI
[


  {


    "id"
: 
"<string>"
,


    "ticker"
: 
"<string>"
,


    "slug"
: 
"<string>"
,


    "title"
: 
"<string>"
,


    "subtitle"
: 
"<string>"
,


    "seriesType"
: 
"<string>"
,


    "recurrence"
: 
"<string>"
,


    "description"
: 
"<string>"
,


    "image"
: 
"<string>"
,


    "icon"
: 
"<string>"
,


    "layout"
: 
"<string>"
,


    "active"
: 
true
,


    "closed"
: 
true
,


    "archived"
: 
true
,


    "new"
: 
true
,


    "featured"
: 
true
,


    "restricted"
: 
true
,


    "isTemplate"
: 
true
,


    "templateVariables"
: 
true
,


    "publishedAt"
: 
"<string>"
,


    "createdBy"
: 
"<string>"
,


    "updatedBy"
: 
"<string>"
,


    "createdAt"
: 
"2023-11-07T05:31:56Z"
,


    "updatedAt"
: 
"2023-11-07T05:31:56Z"
,


    "commentsEnabled"
: 
true
,


    "competitive"
: 
"<string>"
,


    "volume24hr"
: 
123
,


    "volume"
: 
123
,


    "liquidity"
: 
123
,


    "startDate"
: 
"2023-11-07T05:31:56Z"
,


    "pythTokenID"
: 
"<string>"
,


    "cgAssetName"
: 
"<string>"
,


    "score"
: 
123
,


    "events"
: [


      {


        "id"
: 
"<string>"
,


        "ticker"
: 
"<string>"
,


        "slug"
: 
"<string>"
,


        "title"
: 
"<string>"
,


        "subtitle"
: 
"<string>"
,


        "description"
: 
"<string>"
,


        "resolutionSource"
: 
"<string>"
,


        "startDate"
: 
"2023-11-07T05:31:56Z"
,


        "creationDate"
: 
"2023-11-07T05:31:56Z"
,


        "endDate"
: 
"2023-11-07T05:31:56Z"
,


        "image"
: 
"<string>"
,


        "icon"
: 
"<string>"
,


        "active"
: 
true
,


        "closed"
: 
true
,


        "archived"
: 
true
,


        "new"
: 
true
,


        "featured"
: 
true
,


        "restricted"
: 
true
,


        "liquidity"
: 
123
,


        "volume"
: 
123
,


        "openInterest"
: 
123
,


        "sortBy"
: 
"<string>"
,


        "category"
: 
"<string>"
,


        "subcategory"
: 
"<string>"
,


        "isTemplate"
: 
true
,


        "templateVariables"
: 
"<string>"
,


        "published_at"
: 
"<string>"
,


        "createdBy"
: 
"<string>"
,


        "updatedBy"
: 
"<string>"
,


        "createdAt"
: 
"2023-11-07T05:31:56Z"
,


        "updatedAt"
: 
"2023-11-07T05:31:56Z"
,


        "commentsEnabled"
: 
true
,


        "competitive"
: 
123
,


        "volume24hr"
: 
123
,


        "volume1wk"
: 
123
,


        "volume1mo"
: 
123
,


        "volume1yr"
: 
123
,


        "featuredImage"
: 
"<string>"
,


        "disqusThread"
: 
"<string>"
,


        "parentEvent"
: 
"<string>"
,


        "enableOrderBook"
: 
true
,


        "liquidityAmm"
: 
123
,


        "liquidityClob"
: 
123
,


        "negRisk"
: 
true
,


        "negRiskMarketID"
: 
"<string>"
,


        "negRiskFeeBips"
: 
123
,


        "commentCount"
: 
123
,


        "imageOptimized"
: {


          "id"
: 
"<string>"
,


          "imageUrlSource"
: 
"<string>"
,


          "imageUrlOptimized"
: 
"<string>"
,


          "imageSizeKbSource"
: 
123
,


          "imageSizeKbOptimized"
: 
123
,


          "imageOptimizedComplete"
: 
true
,


          "imageOptimizedLastUpdated"
: 
"<string>"
,


          "relID"
: 
123
,


          "field"
: 
"<string>"
,


          "relname"
: 
"<string>"


        },


        "iconOptimized"
: {


          "id"
: 
"<string>"
,


          "imageUrlSource"
: 
"<string>"
,


          "imageUrlOptimized"
: 
"<string>"
,


          "imageSizeKbSource"
: 
123
,


          "imageSizeKbOptimized"
: 
123
,


          "imageOptimizedComplete"
: 
true
,


          "imageOptimizedLastUpdated"
: 
"<string>"
,


          "relID"
: 
123
,


          "field"
: 
"<string>"
,


          "relname"
: 
"<string>"


        },


        "featuredImageOptimized"
: {


          "id"
: 
"<string>"
,


          "imageUrlSource"
: 
"<string>"
,


          "imageUrlOptimized"
: 
"<string>"
,


          "imageSizeKbSource"
: 
123
,


          "imageSizeKbOptimized"
: 
123
,


          "imageOptimizedComplete"
: 
true
,


          "imageOptimizedLastUpdated"
: 
"<string>"
,


          "relID"
: 
123
,


          "field"
: 
"<string>"
,


          "relname"
: 
"<string>"


        },


        "subEvents"
: [


          "<string>"


        ],


        "markets"
: [


          {


            "id"
: 
"<string>"
,


            "question"
: 
"<string>"
,


            "conditionId"
: 
"<string>"
,


            "slug"
: 
"<string>"
,


            "twitterCardImage"
: 
"<string>"
,


            "resolutionSource"
: 
"<string>"
,


            "endDate"
: 
"2023-11-07T05:31:56Z"
,


            "category"
: 
"<string>"
,


            "ammType"
: 
"<string>"
,


            "liquidity"
: 
"<string>"
,


            "sponsorName"
: 
"<string>"
,


            "sponsorImage"
: 
"<string>"
,


            "startDate"
: 
"2023-11-07T05:31:56Z"
,


            "xAxisValue"
: 
"<string>"
,


            "yAxisValue"
: 
"<string>"
,


            "denominationToken"
: 
"<string>"
,


            "fee"
: 
"<string>"
,


            "image"
: 
"<string>"
,


            "icon"
: 
"<string>"
,


            "lowerBound"
: 
"<string>"
,


            "upperBound"
: 
"<string>"
,


            "description"
: 
"<string>"
,


            "outcomes"
: 
"<string>"
,


            "outcomePrices"
: 
"<string>"
,


            "volume"
: 
"<string>"
,


            "active"
: 
true
,


            "marketType"
: 
"<string>"
,


            "formatType"
: 
"<string>"
,


            "lowerBoundDate"
: 
"<string>"
,


            "upperBoundDate"
: 
"<string>"
,


            "closed"
: 
true
,


            "marketMakerAddress"
: 
"<string>"
,


            "createdBy"
: 
123
,


            "updatedBy"
: 
123
,


            "createdAt"
: 
"2023-11-07T05:31:56Z"
,


            "updatedAt"
: 
"2023-11-07T05:31:56Z"
,


            "closedTime"
: 
"<string>"
,


            "wideFormat"
: 
true
,


            "new"
: 
true
,


            "mailchimpTag"
: 
"<string>"
,


            "featured"
: 
true
,


            "archived"
: 
true
,


            "resolvedBy"
: 
"<string>"
,


            "restricted"
: 
true
,


            "marketGroup"
: 
123
,


            "groupItemTitle"
: 
"<string>"
,


            "groupItemThreshold"
: 
"<string>"
,


            "questionID"
: 
"<string>"
,


            "umaEndDate"
: 
"<string>"
,


            "enableOrderBook"
: 
true
,


            "orderPriceMinTickSize"
: 
123
,


            "orderMinSize"
: 
123
,


            "umaResolutionStatus"
: 
"<string>"
,


            "curationOrder"
: 
123
,


            "volumeNum"
: 
123
,


            "liquidityNum"
: 
123
,


            "endDateIso"
: 
"<string>"
,


            "startDateIso"
: 
"<string>"
,


            "umaEndDateIso"
: 
"<string>"
,


            "hasReviewedDates"
: 
true
,


            "readyForCron"
: 
true
,


            "commentsEnabled"
: 
true
,


            "volume24hr"
: 
123
,


            "volume1wk"
: 
123
,


            "volume1mo"
: 
123
,


            "volume1yr"
: 
123
,


            "gameStartTime"
: 
"<string>"
,


            "secondsDelay"
: 
123
,


            "clobTokenIds"
: 
"<string>"
,


            "disqusThread"
: 
"<string>"
,


            "shortOutcomes"
: 
"<string>"
,


            "teamAID"
: 
"<string>"
,


            "teamBID"
: 
"<string>"
,


            "umaBond"
: 
"<string>"
,


            "umaReward"
: 
"<string>"
,


            "fpmmLive"
: 
true
,


            "volume24hrAmm"
: 
123
,


            "volume1wkAmm"
: 
123
,


            "volume1moAmm"
: 
123
,


            "volume1yrAmm"
: 
123
,


            "volume24hrClob"
: 
123
,


            "volume1wkClob"
: 
123
,


            "volume1moClob"
: 
123
,


            "volume1yrClob"
: 
123
,


            "volumeAmm"
: 
123
,


            "volumeClob"
: 
123
,


            "liquidityAmm"
: 
123
,


            "liquidityClob"
: 
123
,


            "makerBaseFee"
: 
123
,


            "takerBaseFee"
: 
123
,


            "customLiveness"
: 
123
,


            "acceptingOrders"
: 
true
,


            "notificationsEnabled"
: 
true
,


            "score"
: 
123
,


            "imageOptimized"
: {


              "id"
: 
"<string>"
,


              "imageUrlSource"
: 
"<string>"
,


              "imageUrlOptimized"
: 
"<string>"
,


              "imageSizeKbSource"
: 
123
,


              "imageSizeKbOptimized"
: 
123
,


              "imageOptimizedComplete"
: 
true
,


              "imageOptimizedLastUpdated"
: 
"<string>"
,


              "relID"
: 
123
,


              "field"
: 
"<string>"
,


              "relname"
: 
"<string>"


            },


            "iconOptimized"
: {


              "id"
: 
"<string>"
,


              "imageUrlSource"
: 
"<string>"
,


              "imageUrlOptimized"
: 
"<string>"
,


              "imageSizeKbSource"
: 
123
,


              "imageSizeKbOptimized"
: 
123
,


              "imageOptimizedComplete"
: 
true
,


              "imageOptimizedLastUpdated"
: 
"<string>"
,


              "relID"
: 
123
,


              "field"
: 
"<string>"
,


              "relname"
: 
"<string>"


            },


            "events"
: 
"<array>"
,


            "categories"
: [


              {


                "id"
: 
"<string>"
,


                "label"
: 
"<string>"
,


                "parentCategory"
: 
"<string>"
,


                "slug"
: 
"<string>"
,


                "publishedAt"
: 
"<string>"
,


                "createdBy"
: 
"<string>"
,


                "updatedBy"
: 
"<string>"
,


                "createdAt"
: 
"2023-11-07T05:31:56Z"
,


                "updatedAt"
: 
"2023-11-07T05:31:56Z"


              }


            ],


            "tags"
: [


              {


                "id"
: 
"<string>"
,


                "label"
: 
"<string>"
,


                "slug"
: 
"<string>"
,


                "forceShow"
: 
true
,


                "publishedAt"
: 
"<string>"
,


                "createdBy"
: 
123
,


                "updatedBy"
: 
123
,


                "createdAt"
: 
"2023-11-07T05:31:56Z"
,


                "updatedAt"
: 
"2023-11-07T05:31:56Z"
,


                "forceHide"
: 
true
,


                "isCarousel"
: 
true


              }


            ],


            "creator"
: 
"<string>"
,


            "ready"
: 
true
,


            "funded"
: 
true
,


            "pastSlugs"
: 
"<string>"
,


            "readyTimestamp"
: 
"2023-11-07T05:31:56Z"
,


            "fundedTimestamp"
: 
"2023-11-07T05:31:56Z"
,


            "acceptingOrdersTimestamp"
: 
"2023-11-07T05:31:56Z"
,


            "competitive"
: 
123
,


            "rewardsMinSize"
: 
123
,


            "rewardsMaxSpread"
: 
123
,


            "spread"
: 
123
,


            "automaticallyResolved"
: 
true
,


            "oneDayPriceChange"
: 
123
,


            "oneHourPriceChange"
: 
123
,


            "oneWeekPriceChange"
: 
123
,


            "oneMonthPriceChange"
: 
123
,


            "oneYearPriceChange"
: 
123
,


            "lastTradePrice"
: 
123
,


            "bestBid"
: 
123
,


            "bestAsk"
: 
123
,


            "automaticallyActive"
: 
true
,


            "clearBookOnStart"
: 
true
,


            "chartColor"
: 
"<string>"
,


            "seriesColor"
: 
"<string>"
,


            "showGmpSeries"
: 
true
,


            "showGmpOutcome"
: 
true
,


            "manualActivation"
: 
true
,


            "negRiskOther"
: 
true
,


            "gameId"
: 
"<string>"
,


            "groupItemRange"
: 
"<string>"
,


            "sportsMarketType"
: 
"<string>"
,


            "line"
: 
123
,


            "umaResolutionStatuses"
: 
"<string>"
,


            "pendingDeployment"
: 
true
,


            "deploying"
: 
true
,


            "deployingTimestamp"
: 
"2023-11-07T05:31:56Z"
,


            "scheduledDeploymentTimestamp"
: 
"2023-11-07T05:31:56Z"
,


            "rfqEnabled"
: 
true
,


            "eventStartTime"
: 
"2023-11-07T05:31:56Z"


          }


        ],


        "series"
: 
"<array>"
,


        "categories"
: [


          {


            "id"
: 
"<string>"
,


            "label"
: 
"<string>"
,


            "parentCategory"
: 
"<string>"
,


            "slug"
: 
"<string>"
,


            "publishedAt"
: 
"<string>"
,


            "createdBy"
: 
"<string>"
,


            "updatedBy"
: 
"<string>"
,


            "createdAt"
: 
"2023-11-07T05:31:56Z"
,


            "updatedAt"
: 
"2023-11-07T05:31:56Z"


          }


        ],


        "collections"
: [


          {


            "id"
: 
"<string>"
,


            "ticker"
: 
"<string>"
,


            "slug"
: 
"<string>"
,


            "title"
: 
"<string>"
,


            "subtitle"
: 
"<string>"
,


            "collectionType"
: 
"<string>"
,


            "description"
: 
"<string>"
,


            "tags"
: 
"<string>"
,


            "image"
: 
"<string>"
,


            "icon"
: 
"<string>"
,


            "headerImage"
: 
"<string>"
,


            "layout"
: 
"<string>"
,


            "active"
: 
true
,


            "closed"
: 
true
,


            "archived"
: 
true
,


            "new"
: 
true
,


            "featured"
: 
true
,


            "restricted"
: 
true
,


            "isTemplate"
: 
true
,


            "templateVariables"
: 
"<string>"
,


            "publishedAt"
: 
"<string>"
,


            "createdBy"
: 
"<string>"
,


            "updatedBy"
: 
"<string>"
,


            "createdAt"
: 
"2023-11-07T05:31:56Z"
,


            "updatedAt"
: 
"2023-11-07T05:31:56Z"
,


            "commentsEnabled"
: 
true
,


            "imageOptimized"
: {


              "id"
: 
"<string>"
,


              "imageUrlSource"
: 
"<string>"
,


              "imageUrlOptimized"
: 
"<string>"
,


              "imageSizeKbSource"
: 
123
,


              "imageSizeKbOptimized"
: 
123
,


              "imageOptimizedComplete"
: 
true
,


              "imageOptimizedLastUpdated"
: 
"<string>"
,


              "relID"
: 
123
,


              "field"
: 
"<string>"
,


              "relname"
: 
"<string>"


            },


            "iconOptimized"
: {


              "id"
: 
"<string>"
,


              "imageUrlSource"
: 
"<string>"
,


              "imageUrlOptimized"
: 
"<string>"
,


              "imageSizeKbSource"
: 
123
,


              "imageSizeKbOptimized"
: 
123
,


              "imageOptimizedComplete"
: 
true
,


              "imageOptimizedLastUpdated"
: 
"<string>"
,


              "relID"
: 
123
,


              "field"
: 
"<string>"
,


              "relname"
: 
"<string>"


            },


            "headerImageOptimized"
: {


              "id"
: 
"<string>"
,


              "imageUrlSource"
: 
"<string>"
,


              "imageUrlOptimized"
: 
"<string>"
,


              "imageSizeKbSource"
: 
123
,


              "imageSizeKbOptimized"
: 
123
,


              "imageOptimizedComplete"
: 
true
,


              "imageOptimizedLastUpdated"
: 
"<string>"
,


              "relID"
: 
123
,


              "field"
: 
"<string>"
,


              "relname"
: 
"<string>"


            }


          }


        ],


        "tags"
: [


          {


            "id"
: 
"<string>"
,


            "label"
: 
"<string>"
,


            "slug"
: 
"<string>"
,


            "forceShow"
: 
true
,


            "publishedAt"
: 
"<string>"
,


            "createdBy"
: 
123
,


            "updatedBy"
: 
123
,


            "createdAt"
: 
"2023-11-07T05:31:56Z"
,


            "updatedAt"
: 
"2023-11-07T05:31:56Z"
,


            "forceHide"
: 
true
,


            "isCarousel"
: 
true


          }


        ],


        "cyom"
: 
true
,


        "closedTime"
: 
"2023-11-07T05:31:56Z"
,


        "showAllOutcomes"
: 
true
,


        "showMarketImages"
: 
true
,


        "automaticallyResolved"
: 
true
,


        "enableNegRisk"
: 
true
,


        "automaticallyActive"
: 
true
,


        "eventDate"
: 
"<string>"
,


        "startTime"
: 
"2023-11-07T05:31:56Z"
,


        "eventWeek"
: 
123
,


        "seriesSlug"
: 
"<string>"
,


        "score"
: 
"<string>"
,


        "elapsed"
: 
"<string>"
,


        "period"
: 
"<string>"
,


        "live"
: 
true
,


        "ended"
: 
true
,


        "finishedTimestamp"
: 
"2023-11-07T05:31:56Z"
,


        "gmpChartMode"
: 
"<string>"
,


        "eventCreators"
: [


          {


            "id"
: 
"<string>"
,


            "creatorName"
: 
"<string>"
,


            "creatorHandle"
: 
"<string>"
,


            "creatorUrl"
: 
"<string>"
,


            "creatorImage"
: 
"<string>"
,


            "createdAt"
: 
"2023-11-07T05:31:56Z"
,


            "updatedAt"
: 
"2023-11-07T05:31:56Z"


          }


        ],


        "tweetCount"
: 
123
,


        "chats"
: [


          {


            "id"
: 
"<string>"
,


            "channelId"
: 
"<string>"
,


            "channelName"
: 
"<string>"
,


            "channelImage"
: 
"<string>"
,


            "live"
: 
true
,


            "startTime"
: 
"2023-11-07T05:31:56Z"
,


            "endTime"
: 
"2023-11-07T05:31:56Z"


          }


        ],


        "featuredOrder"
: 
123
,


        "estimateValue"
: 
true
,


        "cantEstimate"
: 
true
,


        "estimatedValue"
: 
"<string>"
,


        "templates"
: [


          {


            "id"
: 
"<string>"
,


            "eventTitle"
: 
"<string>"
,


            "eventSlug"
: 
"<string>"
,


            "eventImage"
: 
"<string>"
,


            "marketTitle"
: 
"<string>"
,


            "description"
: 
"<string>"
,


            "resolutionSource"
: 
"<string>"
,


            "negRisk"
: 
true
,


            "sortBy"
: 
"<string>"
,


            "showMarketImages"
: 
true
,


            "seriesSlug"
: 
"<string>"
,


            "outcomes"
: 
"<string>"


          }


        ],


        "spreadsMainLine"
: 
123
,


        "totalsMainLine"
: 
123
,


        "carouselMap"
: 
"<string>"
,


        "pendingDeployment"
: 
true
,


        "deploying"
: 
true
,


        "deployingTimestamp"
: 
"2023-11-07T05:31:56Z"
,


        "scheduledDeploymentTimestamp"
: 
"2023-11-07T05:31:56Z"
,


        "gameStatus"
: 
"<string>"


      }


    ],


    "collections"
: [


      {


        "id"
: 
"<string>"
,


        "ticker"
: 
"<string>"
,


        "slug"
: 
"<string>"
,


        "title"
: 
"<string>"
,


        "subtitle"
: 
"<string>"
,


        "collectionType"
: 
"<string>"
,


        "description"
: 
"<string>"
,


        "tags"
: 
"<string>"
,


        "image"
: 
"<string>"
,


        "icon"
: 
"<string>"
,


        "headerImage"
: 
"<string>"
,


        "layout"
: 
"<string>"
,


        "active"
: 
true
,


        "closed"
: 
true
,


        "archived"
: 
true
,


        "new"
: 
true
,


        "featured"
: 
true
,


        "restricted"
: 
true
,


        "isTemplate"
: 
true
,


        "templateVariables"
: 
"<string>"
,


        "publishedAt"
: 
"<string>"
,


        "createdBy"
: 
"<string>"
,


        "updatedBy"
: 
"<string>"
,


        "createdAt"
: 
"2023-11-07T05:31:56Z"
,


        "updatedAt"
: 
"2023-11-07T05:31:56Z"
,


        "commentsEnabled"
: 
true
,


        "imageOptimized"
: {


          "id"
: 
"<string>"
,


          "imageUrlSource"
: 
"<string>"
,


          "imageUrlOptimized"
: 
"<string>"
,


          "imageSizeKbSource"
: 
123
,


          "imageSizeKbOptimized"
: 
123
,


          "imageOptimizedComplete"
: 
true
,


          "imageOptimizedLastUpdated"
: 
"<string>"
,


          "relID"
: 
123
,


          "field"
: 
"<string>"
,


          "relname"
: 
"<string>"


        },


        "iconOptimized"
: {


          "id"
: 
"<string>"
,


          "imageUrlSource"
: 
"<string>"
,


          "imageUrlOptimized"
: 
"<string>"
,


          "imageSizeKbSource"
: 
123
,


          "imageSizeKbOptimized"
: 
123
,


          "imageOptimizedComplete"
: 
true
,


          "imageOptimizedLastUpdated"
: 
"<string>"
,


          "relID"
: 
123
,


          "field"
: 
"<string>"
,


          "relname"
: 
"<string>"


        },


        "headerImageOptimized"
: {


          "id"
: 
"<string>"
,


          "imageUrlSource"
: 
"<string>"
,


          "imageUrlOptimized"
: 
"<string>"
,


          "imageSizeKbSource"
: 
123
,


          "imageSizeKbOptimized"
: 
123
,


          "imageOptimizedComplete"
: 
true
,


          "imageOptimizedLastUpdated"
: 
"<string>"
,


          "relID"
: 
123
,


          "field"
: 
"<string>"
,


          "relname"
: 
"<string>"


        }


      }


    ],


    "categories"
: [


      {


        "id"
: 
"<string>"
,


        "label"
: 
"<string>"
,


        "parentCategory"
: 
"<string>"
,


        "slug"
: 
"<string>"
,


        "publishedAt"
: 
"<string>"
,


        "createdBy"
: 
"<string>"
,


        "updatedBy"
: 
"<string>"
,


        "createdAt"
: 
"2023-11-07T05:31:56Z"
,


        "updatedAt"
: 
"2023-11-07T05:31:56Z"


      }


    ],


    "tags"
: [


      {


        "id"
: 
"<string>"
,


        "label"
: 
"<string>"
,


        "slug"
: 
"<string>"
,


        "forceShow"
: 
true
,


        "publishedAt"
: 
"<string>"
,


        "createdBy"
: 
123
,


        "updatedBy"
: 
123
,


        "createdAt"
: 
"2023-11-07T05:31:56Z"
,


        "updatedAt"
: 
"2023-11-07T05:31:56Z"
,


        "forceHide"
: 
true
,


        "isCarousel"
: 
true


      }


    ],


    "commentCount"
: 
123
,


    "chats"
: [


      {


        "id"
: 
"<string>"
,


        "channelId"
: 
"<string>"
,


        "channelName"
: 
"<string>"
,


        "channelImage"
: 
"<string>"
,


        "live"
: 
true
,


        "startTime"
: 
"2023-11-07T05:31:56Z"
,


        "endTime"
: 
"2023-11-07T05:31:56Z"


      }


    ]


  }


]
Query Parameters
​
limit
integer
Required range
: 
x >= 0
​
offset
integer
Required range
: 
x >= 0
​
order
string
Comma-separated list of fields to order by
​
ascending
boolean
​
slug
string[]
​
categories_ids
integer[]
​
categories_labels
string[]
​
closed
boolean
​
include_chat
boolean
​
recurrence
string
Response
200 - application/json
List of series
​
id
string
​
ticker
string | null
​
slug
string | null
​
title
string | null
​
subtitle
string | null
​
seriesType
string | null
​
recurrence
string | null
​
description
string | null
​
image
string | null
​
icon
string | null
​
layout
string | null
​
active
boolean | null
​
closed
boolean | null
​
archived
boolean | null
​
new
boolean | null
​
featured
boolean | null
​
restricted
boolean | null
​
isTemplate
boolean | null
​
templateVariables
boolean | null
​
publishedAt
string | null
​
createdBy
string | null
​
updatedBy
string | null
​
createdAt
string<date-time> | null
​
updatedAt
string<date-time> | null
​
commentsEnabled
boolean | null
​
competitive
string | null
​
volume24hr
number | null
​
volume
number | null
​
liquidity
number | null
​
startDate
string<date-time> | null
​
pythTokenID
string | null
​
cgAssetName
string | null
​
score
integer | null
​
events
object[]
Show
 
child attributes
​
events.
id
string
​
events.
ticker
string | null
​
events.
slug
string | null
​
events.
title
string | null
​
events.
subtitle
string | null
​
events.
description
string | null
​
events.
resolutionSource
string | null
​
events.
startDate
string<date-time> | null
​
events.
creationDate
string<date-time> | null
​
events.
endDate
string<date-time> | null
​
events.
image
string | null
​
events.
icon
string | null
​
events.
active
boolean | null
​
events.
closed
boolean | null
​
events.
archived
boolean | null
​
events.
new
boolean | null
​
events.
featured
boolean | null
​
events.
restricted
boolean | null
​
events.
liquidity
number | null
​
events.
volume
number | null
​
events.
openInterest
number | null
​
events.
sortBy
string | null
​
events.
category
string | null
​
events.
subcategory
string | null
​
events.
isTemplate
boolean | null
​
events.
templateVariables
string | null
​
events.
published_at
string | null
​
events.
createdBy
string | null
​
events.
updatedBy
string | null
​
events.
createdAt
string<date-time> | null
​
events.
updatedAt
string<date-time> | null
​
events.
commentsEnabled
boolean | null
​
events.
competitive
number | null
​
events.
volume24hr
number | null
​
events.
volume1wk
number | null
​
events.
volume1mo
number | null
​
events.
volume1yr
number | null
​
events.
featuredImage
string | null
​
events.
disqusThread
string | null
​
events.
parentEvent
string | null
​
events.
enableOrderBook
boolean | null
​
events.
liquidityAmm
number | null
​
events.
liquidityClob
number | null
​
events.
negRisk
boolean | null
​
events.
negRiskMarketID
string | null
​
events.
negRiskFeeBips
integer | null
​
events.
commentCount
integer | null
​
events.
imageOptimized
object
Show
 
child attributes
​
events.imageOptimized.
id
string
​
events.imageOptimized.
imageUrlSource
string | null
​
events.imageOptimized.
imageUrlOptimized
string | null
​
events.imageOptimized.
imageSizeKbSource
number | null
​
events.imageOptimized.
imageSizeKbOptimized
number | null
​
events.imageOptimized.
imageOptimizedComplete
boolean | null
​
events.imageOptimized.
imageOptimizedLastUpdated
string | null
​
events.imageOptimized.
relID
integer | null
​
events.imageOptimized.
field
string | null
​
events.imageOptimized.
relname
string | null
​
events.
iconOptimized
object
Show
 
child attributes
​
events.iconOptimized.
id
string
​
events.iconOptimized.
imageUrlSource
string | null
​
events.iconOptimized.
imageUrlOptimized
string | null
​
events.iconOptimized.
imageSizeKbSource
number | null
​
events.iconOptimized.
imageSizeKbOptimized
number | null
​
events.iconOptimized.
imageOptimizedComplete
boolean | null
​
events.iconOptimized.
imageOptimizedLastUpdated
string | null
​
events.iconOptimized.
relID
integer | null
​
events.iconOptimized.
field
string | null
​
events.iconOptimized.
relname
string | null
​
events.
featuredImageOptimized
object
Show
 
child attributes
​
events.featuredImageOptimized.
id
string
​
events.featuredImageOptimized.
imageUrlSource
string | null
​
events.featuredImageOptimized.
imageUrlOptimized
string | null
​
events.featuredImageOptimized.
imageSizeKbSource
number | null
​
events.featuredImageOptimized.
imageSizeKbOptimized
number | null
​
events.featuredImageOptimized.
imageOptimizedComplete
boolean | null
​
events.featuredImageOptimized.
imageOptimizedLastUpdated
string | null
​
events.featuredImageOptimized.
relID
integer | null
​
events.featuredImageOptimized.
field
string | null
​
events.featuredImageOptimized.
relname
string | null
​
events.
subEvents
string[] | null
​
events.
markets
object[]
Show
 
child attributes
​
events.markets.
id
string
​
events.markets.
question
string | null
​
events.markets.
conditionId
string
​
events.markets.
slug
string | null
​
events.markets.
twitterCardImage
string | null
​
events.markets.
resolutionSource
string | null
​
events.markets.
endDate
string<date-time> | null
​
events.markets.
category
string | null
​
events.markets.
ammType
string | null
​
events.markets.
liquidity
string | null
​
events.markets.
sponsorName
string | null
​
events.markets.
sponsorImage
string | null
​
events.markets.
startDate
string<date-time> | null
​
events.markets.
xAxisValue
string | null
​
events.markets.
yAxisValue
string | null
​
events.markets.
denominationToken
string | null
​
events.markets.
fee
string | null
​
events.markets.
image
string | null
​
events.markets.
icon
string | null
​
events.markets.
lowerBound
string | null
​
events.markets.
upperBound
string | null
​
events.markets.
description
string | null
​
events.markets.
outcomes
string | null
​
events.markets.
outcomePrices
string | null
​
events.markets.
volume
string | null
​
events.markets.
active
boolean | null
​
events.markets.
marketType
string | null
​
events.markets.
formatType
string | null
​
events.markets.
lowerBoundDate
string | null
​
events.markets.
upperBoundDate
string | null
​
events.markets.
closed
boolean | null
​
events.markets.
marketMakerAddress
string
​
events.markets.
createdBy
integer | null
​
events.markets.
updatedBy
integer | null
​
events.markets.
createdAt
string<date-time> | null
​
events.markets.
updatedAt
string<date-time> | null
​
events.markets.
closedTime
string | null
​
events.markets.
wideFormat
boolean | null
​
events.markets.
new
boolean | null
​
events.markets.
mailchimpTag
string | null
​
events.markets.
featured
boolean | null
​
events.markets.
archived
boolean | null
​
events.markets.
resolvedBy
string | null
​
events.markets.
restricted
boolean | null
​
events.markets.
marketGroup
integer | null
​
events.markets.
groupItemTitle
string | null
​
events.markets.
groupItemThreshold
string | null
​
events.markets.
questionID
string | null
​
events.markets.
umaEndDate
string | null
​
events.markets.
enableOrderBook
boolean | null
​
events.markets.
orderPriceMinTickSize
number | null
​
events.markets.
orderMinSize
number | null
​
events.markets.
umaResolutionStatus
string | null
​
events.markets.
curationOrder
integer | null
​
events.markets.
volumeNum
number | null
​
events.markets.
liquidityNum
number | null
​
events.markets.
endDateIso
string | null
​
events.markets.
startDateIso
string | null
​
events.markets.
umaEndDateIso
string | null
​
events.markets.
hasReviewedDates
boolean | null
​
events.markets.
readyForCron
boolean | null
​
events.markets.
commentsEnabled
boolean | null
​
events.markets.
volume24hr
number | null
​
events.markets.
volume1wk
number | null
​
events.markets.
volume1mo
number | null
​
events.markets.
volume1yr
number | null
​
events.markets.
gameStartTime
string | null
​
events.markets.
secondsDelay
integer | null
​
events.markets.
clobTokenIds
string | null
​
events.markets.
disqusThread
string | null
​
events.markets.
shortOutcomes
string | null
​
events.markets.
teamAID
string | null
​
events.markets.
teamBID
string | null
​
events.markets.
umaBond
string | null
​
events.markets.
umaReward
string | null
​
events.markets.
fpmmLive
boolean | null
​
events.markets.
volume24hrAmm
number | null
​
events.markets.
volume1wkAmm
number | null
​
events.markets.
volume1moAmm
number | null
​
events.markets.
volume1yrAmm
number | null
​
events.markets.
volume24hrClob
number | null
​
events.markets.
volume1wkClob
number | null
​
events.markets.
volume1moClob
number | null
​
events.markets.
volume1yrClob
number | null
​
events.markets.
volumeAmm
number | null
​
events.markets.
volumeClob
number | null
​
events.markets.
liquidityAmm
number | null
​
events.markets.
liquidityClob
number | null
​
events.markets.
makerBaseFee
integer | null
​
events.markets.
takerBaseFee
integer | null
​
events.markets.
customLiveness
integer | null
​
events.markets.
acceptingOrders
boolean | null
​
events.markets.
notificationsEnabled
boolean | null
​
events.markets.
score
integer | null
​
events.markets.
imageOptimized
object
Show
 
child attributes
​
events.markets.imageOptimized.
id
string
​
events.markets.imageOptimized.
imageUrlSource
string | null
​
events.markets.imageOptimized.
imageUrlOptimized
string | null
​
events.markets.imageOptimized.
imageSizeKbSource
number | null
​
events.markets.imageOptimized.
imageSizeKbOptimized
number | null
​
events.markets.imageOptimized.
imageOptimizedComplete
boolean | null
​
events.markets.imageOptimized.
imageOptimizedLastUpdated
string | null
​
events.markets.imageOptimized.
relID
integer | null
​
events.markets.imageOptimized.
field
string | null
​
events.markets.imageOptimized.
relname
string | null
​
events.markets.
iconOptimized
object
Show
 
child attributes
​
events.markets.iconOptimized.
id
string
​
events.markets.iconOptimized.
imageUrlSource
string | null
​
events.markets.iconOptimized.
imageUrlOptimized
string | null
​
events.markets.iconOptimized.
imageSizeKbSource
number | null
​
events.markets.iconOptimized.
imageSizeKbOptimized
number | null
​
events.markets.iconOptimized.
imageOptimizedComplete
boolean | null
​
events.markets.iconOptimized.
imageOptimizedLastUpdated
string | null
​
events.markets.iconOptimized.
relID
integer | null
​
events.markets.iconOptimized.
field
string | null
​
events.markets.iconOptimized.
relname
string | null
​
events.markets.
events
array
​
events.markets.
categories
object[]
Show
 
child attributes
​
events.markets.categories.
id
string
​
events.markets.categories.
label
string | null
​
events.markets.categories.
parentCategory
string | null
​
events.markets.categories.
slug
string | null
​
events.markets.categories.
publishedAt
string | null
​
events.markets.categories.
createdBy
string | null
​
events.markets.categories.
updatedBy
string | null
​
events.markets.categories.
createdAt
string<date-time> | null
​
events.markets.categories.
updatedAt
string<date-time> | null
​
events.markets.
tags
object[]
Show
 
child attributes
​
events.markets.tags.
id
string
​
events.markets.tags.
label
string | null
​
events.markets.tags.
slug
string | null
​
events.markets.tags.
forceShow
boolean | null
​
events.markets.tags.
publishedAt
string | null
​
events.markets.tags.
createdBy
integer | null
​
events.markets.tags.
updatedBy
integer | null
​
events.markets.tags.
createdAt
string<date-time> | null
​
events.markets.tags.
updatedAt
string<date-time> | null
​
events.markets.tags.
forceHide
boolean | null
​
events.markets.tags.
isCarousel
boolean | null
​
events.markets.
creator
string | null
​
events.markets.
ready
boolean | null
​
events.markets.
funded
boolean | null
​
events.markets.
pastSlugs
string | null
​
events.markets.
readyTimestamp
string<date-time> | null
​
events.markets.
fundedTimestamp
string<date-time> | null
​
events.markets.
acceptingOrdersTimestamp
string<date-time> | null
​
events.markets.
competitive
number | null
​
events.markets.
rewardsMinSize
number | null
​
events.markets.
rewardsMaxSpread
number | null
​
events.markets.
spread
number | null
​
events.markets.
automaticallyResolved
boolean | null
​
events.markets.
oneDayPriceChange
number | null
​
events.markets.
oneHourPriceChange
number | null
​
events.markets.
oneWeekPriceChange
number | null
​
events.markets.
oneMonthPriceChange
number | null
​
events.markets.
oneYearPriceChange
number | null
​
events.markets.
lastTradePrice
number | null
​
events.markets.
bestBid
number | null
​
events.markets.
bestAsk
number | null
​
events.markets.
automaticallyActive
boolean | null
​
events.markets.
clearBookOnStart
boolean | null
​
events.markets.
chartColor
string | null
​
events.markets.
seriesColor
string | null
​
events.markets.
showGmpSeries
boolean | null
​
events.markets.
showGmpOutcome
boolean | null
​
events.markets.
manualActivation
boolean | null
​
events.markets.
negRiskOther
boolean | null
​
events.markets.
gameId
string | null
​
events.markets.
groupItemRange
string | null
​
events.markets.
sportsMarketType
string | null
​
events.markets.
line
number | null
​
events.markets.
umaResolutionStatuses
string | null
​
events.markets.
pendingDeployment
boolean | null
​
events.markets.
deploying
boolean | null
​
events.markets.
deployingTimestamp
string<date-time> | null
​
events.markets.
scheduledDeploymentTimestamp
string<date-time> | null
​
events.markets.
rfqEnabled
boolean | null
​
events.markets.
eventStartTime
string<date-time> | null
​
events.
series
array
​
events.
categories
object[]
Show
 
child attributes
​
events.categories.
id
string
​
events.categories.
label
string | null
​
events.categories.
parentCategory
string | null
​
events.categories.
slug
string | null
​
events.categories.
publishedAt
string | null
​
events.categories.
createdBy
string | null
​
events.categories.
updatedBy
string | null
​
events.categories.
createdAt
string<date-time> | null
​
events.categories.
updatedAt
string<date-time> | null
​
events.
collections
object[]
Show
 
child attributes
​
events.collections.
id
string
​
events.collections.
ticker
string | null
​
events.collections.
slug
string | null
​
events.collections.
title
string | null
​
events.collections.
subtitle
string | null
​
events.collections.
collectionType
string | null
​
events.collections.
description
string | null
​
events.collections.
tags
string | null
​
events.collections.
image
string | null
​
events.collections.
icon
string | null
​
events.collections.
headerImage
string | null
​
events.collections.
layout
string | null
​
events.collections.
active
boolean | null
​
events.collections.
closed
boolean | null
​
events.collections.
archived
boolean | null
​
events.collections.
new
boolean | null
​
events.collections.
featured
boolean | null
​
events.collections.
restricted
boolean | null
​
events.collections.
isTemplate
boolean | null
​
events.collections.
templateVariables
string | null
​
events.collections.
publishedAt
string | null
​
events.collections.
createdBy
string | null
​
events.collections.
updatedBy
string | null
​
events.collections.
createdAt
string<date-time> | null
​
events.collections.
updatedAt
string<date-time> | null
​
events.collections.
commentsEnabled
boolean | null
​
events.collections.
imageOptimized
object
Show
 
child attributes
​
events.collections.imageOptimized.
id
string
​
events.collections.imageOptimized.
imageUrlSource
string | null
​
events.collections.imageOptimized.
imageUrlOptimized
string | null
​
events.collections.imageOptimized.
imageSizeKbSource
number | null
​
events.collections.imageOptimized.
imageSizeKbOptimized
number | null
​
events.collections.imageOptimized.
imageOptimizedComplete
boolean | null
​
events.collections.imageOptimized.
imageOptimizedLastUpdated
string | null
​
events.collections.imageOptimized.
relID
integer | null
​
events.collections.imageOptimized.
field
string | null
​
events.collections.imageOptimized.
relname
string | null
​
events.collections.
iconOptimized
object
Show
 
child attributes
​
events.collections.iconOptimized.
id
string
​
events.collections.iconOptimized.
imageUrlSource
string | null
​
events.collections.iconOptimized.
imageUrlOptimized
string | null
​
events.collections.iconOptimized.
imageSizeKbSource
number | null
​
events.collections.iconOptimized.
imageSizeKbOptimized
number | null
​
events.collections.iconOptimized.
imageOptimizedComplete
boolean | null
​
events.collections.iconOptimized.
imageOptimizedLastUpdated
string | null
​
events.collections.iconOptimized.
relID
integer | null
​
events.collections.iconOptimized.
field
string | null
​
events.collections.iconOptimized.
relname
string | null
​
events.collections.
headerImageOptimized
object
Show
 
child attributes
​
events.collections.headerImageOptimized.
id
string
​
events.collections.headerImageOptimized.
imageUrlSource
string | null
​
events.collections.headerImageOptimized.
imageUrlOptimized
string | null
​
events.collections.headerImageOptimized.
imageSizeKbSource
number | null
​
events.collections.headerImageOptimized.
imageSizeKbOptimized
number | null
​
events.collections.headerImageOptimized.
imageOptimizedComplete
boolean | null
​
events.collections.headerImageOptimized.
imageOptimizedLastUpdated
string | null
​
events.collections.headerImageOptimized.
relID
integer | null
​
events.collections.headerImageOptimized.
field
string | null
​
events.collections.headerImageOptimized.
relname
string | null
​
events.
tags
object[]
Show
 
child attributes
​
events.tags.
id
string
​
events.tags.
label
string | null
​
events.tags.
slug
string | null
​
events.tags.
forceShow
boolean | null
​
events.tags.
publishedAt
string | null
​
events.tags.
createdBy
integer | null
​
events.tags.
updatedBy
integer | null
​
events.tags.
createdAt
string<date-time> | null
​
events.tags.
updatedAt
string<date-time> | null
​
events.tags.
forceHide
boolean | null
​
events.tags.
isCarousel
boolean | null
​
events.
cyom
boolean | null
​
events.
closedTime
string<date-time> | null
​
events.
showAllOutcomes
boolean | null
​
events.
showMarketImages
boolean | null
​
events.
automaticallyResolved
boolean | null
​
events.
enableNegRisk
boolean | null
​
events.
automaticallyActive
boolean | null
​
events.
eventDate
string | null
​
events.
startTime
string<date-time> | null
​
events.
eventWeek
integer | null
​
events.
seriesSlug
string | null
​
events.
score
string | null
​
events.
elapsed
string | null
​
events.
period
string | null
​
events.
live
boolean | null
​
events.
ended
boolean | null
​
events.
finishedTimestamp
string<date-time> | null
​
events.
gmpChartMode
string | null
​
events.
eventCreators
object[]
Show
 
child attributes
​
events.eventCreators.
id
string
​
events.eventCreators.
creatorName
string | null
​
events.eventCreators.
creatorHandle
string | null
​
events.eventCreators.
creatorUrl
string | null
​
events.eventCreators.
creatorImage
string | null
​
events.eventCreators.
createdAt
string<date-time> | null
​
events.eventCreators.
updatedAt
string<date-time> | null
​
events.
tweetCount
integer | null
​
events.
chats
object[]
Show
 
child attributes
​
events.chats.
id
string
​
events.chats.
channelId
string | null
​
events.chats.
channelName
string | null
​
events.chats.
channelImage
string | null
​
events.chats.
live
boolean | null
​
events.chats.
startTime
string<date-time> | null
​
events.chats.
endTime
string<date-time> | null
​
events.
featuredOrder
integer | null
​
events.
estimateValue
boolean | null
​
events.
cantEstimate
boolean | null
​
events.
estimatedValue
string | null
​
events.
templates
object[]
Show
 
child attributes
​
events.templates.
id
string
​
events.templates.
eventTitle
string | null
​
events.templates.
eventSlug
string | null
​
events.templates.
eventImage
string | null
​
events.templates.
marketTitle
string | null
​
events.templates.
description
string | null
​
events.templates.
resolutionSource
string | null
​
events.templates.
negRisk
boolean | null
​
events.templates.
sortBy
string | null
​
events.templates.
showMarketImages
boolean | null
​
events.templates.
seriesSlug
string | null
​
events.templates.
outcomes
string | null
​
events.
spreadsMainLine
number | null
​
events.
totalsMainLine
number | null
​
events.
carouselMap
string | null
​
events.
pendingDeployment
boolean | null
​
events.
deploying
boolean | null
​
events.
deployingTimestamp
string<date-time> | null
​
events.
scheduledDeploymentTimestamp
string<date-time> | null
​
events.
gameStatus
string | null
​
collections
object[]
Show
 
child attributes
​
collections.
id
string
​
collections.
ticker
string | null
​
collections.
slug
string | null
​
collections.
title
string | null
​
collections.
subtitle
string | null
​
collections.
collectionType
string | null
​
collections.
description
string | null
​
collections.
tags
string | null
​
collections.
image
string | null
​
collections.
icon
string | null
​
collections.
headerImage
string | null
​
collections.
layout
string | null
​
collections.
active
boolean | null
​
collections.
closed
boolean | null
​
collections.
archived
boolean | null
​
collections.
new
boolean | null
​
collections.
featured
boolean | null
​
collections.
restricted
boolean | null
​
collections.
isTemplate
boolean | null
​
collections.
templateVariables
string | null
​
collections.
publishedAt
string | null
​
collections.
createdBy
string | null
​
collections.
updatedBy
string | null
​
collections.
createdAt
string<date-time> | null
​
collections.
updatedAt
string<date-time> | null
​
collections.
commentsEnabled
boolean | null
​
collections.
imageOptimized
object
Show
 
child attributes
​
collections.imageOptimized.
id
string
​
collections.imageOptimized.
imageUrlSource
string | null
​
collections.imageOptimized.
imageUrlOptimized
string | null
​
collections.imageOptimized.
imageSizeKbSource
number | null
​
collections.imageOptimized.
imageSizeKbOptimized
number | null
​
collections.imageOptimized.
imageOptimizedComplete
boolean | null
​
collections.imageOptimized.
imageOptimizedLastUpdated
string | null
​
collections.imageOptimized.
relID
integer | null
​
collections.imageOptimized.
field
string | null
​
collections.imageOptimized.
relname
string | null
​
collections.
iconOptimized
object
Show
 
child attributes
​
collections.iconOptimized.
id
string
​
collections.iconOptimized.
imageUrlSource
string | null
​
collections.iconOptimized.
imageUrlOptimized
string | null
​
collections.iconOptimized.
imageSizeKbSource
number | null
​
collections.iconOptimized.
imageSizeKbOptimized
number | null
​
collections.iconOptimized.
imageOptimizedComplete
boolean | null
​
collections.iconOptimized.
imageOptimizedLastUpdated
string | null
​
collections.iconOptimized.
relID
integer | null
​
collections.iconOptimized.
field
string | null
​
collections.iconOptimized.
relname
string | null
​
collections.
headerImageOptimized
object
Show
 
child attributes
​
collections.headerImageOptimized.
id
string
​
collections.headerImageOptimized.
imageUrlSource
string | null
​
collections.headerImageOptimized.
imageUrlOptimized
string | null
​
collections.headerImageOptimized.
imageSizeKbSource
number | null
​
collections.headerImageOptimized.
imageSizeKbOptimized
number | null
​
collections.headerImageOptimized.
imageOptimizedComplete
boolean | null
​
collections.headerImageOptimized.
imageOptimizedLastUpdated
string | null
​
collections.headerImageOptimized.
relID
integer | null
​
collections.headerImageOptimized.
field
string | null
​
collections.headerImageOptimized.
relname
string | null
​
categories
object[]
Show
 
child attributes
​
categories.
id
string
​
categories.
label
string | null
​
categories.
parentCategory
string | null
​
categories.
slug
string | null
​
categories.
publishedAt
string | null
​
categories.
createdBy
string | null
​
categories.
updatedBy
string | null
​
categories.
createdAt
string<date-time> | null
​
categories.
updatedAt
string<date-time> | null
​
tags
object[]
Show
 
child attributes
​
tags.
id
string
​
tags.
label
string | null
​
tags.
slug
string | null
​
tags.
forceShow
boolean | null
​
tags.
publishedAt
string | null
​
tags.
createdBy
integer | null
​
tags.
updatedBy
integer | null
​
tags.
createdAt
string<date-time> | null
​
tags.
updatedAt
string<date-time> | null
​
tags.
forceHide
boolean | null
​
tags.
isCarousel
boolean | null
​
commentCount
integer | null
​
chats
object[]
Show
 
child attributes
​
chats.
id
string
​
chats.
channelId
string | null
​
chats.
channelName
string | null
​
chats.
channelImage
string | null
​
chats.
live
boolean | null
​
chats.
startTime
string<date-time> | null
​
chats.
endTime
string<date-time> | null
Get market by slug
Get series by id
⌘
I
github
Powered by Mintlify