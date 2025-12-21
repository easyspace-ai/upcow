# List events - Polymarket Documentation

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
Events
List events
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
GET
List events
GET
Get event by id
GET
Get event tags
GET
Get event by slug
Markets
Series
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
List events
cURL
Copy
Ask AI
curl
 --request
 GET
 \


  --url
 https://gamma-api.polymarket.com/events
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
: 
"<array>"
,


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


]
Events
List events
GET
/
events
Try it
List events
cURL
Copy
Ask AI
curl
 --request
 GET
 \


  --url
 https://gamma-api.polymarket.com/events
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
: 
"<array>"
,


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
id
integer[]
​
tag_id
integer
​
exclude_tag_id
integer[]
​
slug
string[]
​
tag_slug
string
​
related_tags
boolean
​
active
boolean
​
archived
boolean
​
featured
boolean
​
cyom
boolean
​
include_chat
boolean
​
include_template
boolean
​
recurrence
string
​
closed
boolean
​
liquidity_min
number
​
liquidity_max
number
​
volume_min
number
​
volume_max
number
​
start_date_min
string<date-time>
​
start_date_max
string<date-time>
​
end_date_min
string<date-time>
​
end_date_max
string<date-time>
Response
200 - application/json
List of events
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
description
string | null
​
resolutionSource
string | null
​
startDate
string<date-time> | null
​
creationDate
string<date-time> | null
​
endDate
string<date-time> | null
​
image
string | null
​
icon
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
liquidity
number | null
​
volume
number | null
​
openInterest
number | null
​
sortBy
string | null
​
category
string | null
​
subcategory
string | null
​
isTemplate
boolean | null
​
templateVariables
string | null
​
published_at
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
number | null
​
volume24hr
number | null
​
volume1wk
number | null
​
volume1mo
number | null
​
volume1yr
number | null
​
featuredImage
string | null
​
disqusThread
string | null
​
parentEvent
string | null
​
enableOrderBook
boolean | null
​
liquidityAmm
number | null
​
liquidityClob
number | null
​
negRisk
boolean | null
​
negRiskMarketID
string | null
​
negRiskFeeBips
integer | null
​
commentCount
integer | null
​
imageOptimized
object
Show
 
child attributes
​
imageOptimized.
id
string
​
imageOptimized.
imageUrlSource
string | null
​
imageOptimized.
imageUrlOptimized
string | null
​
imageOptimized.
imageSizeKbSource
number | null
​
imageOptimized.
imageSizeKbOptimized
number | null
​
imageOptimized.
imageOptimizedComplete
boolean | null
​
imageOptimized.
imageOptimizedLastUpdated
string | null
​
imageOptimized.
relID
integer | null
​
imageOptimized.
field
string | null
​
imageOptimized.
relname
string | null
​
iconOptimized
object
Show
 
child attributes
​
iconOptimized.
id
string
​
iconOptimized.
imageUrlSource
string | null
​
iconOptimized.
imageUrlOptimized
string | null
​
iconOptimized.
imageSizeKbSource
number | null
​
iconOptimized.
imageSizeKbOptimized
number | null
​
iconOptimized.
imageOptimizedComplete
boolean | null
​
iconOptimized.
imageOptimizedLastUpdated
string | null
​
iconOptimized.
relID
integer | null
​
iconOptimized.
field
string | null
​
iconOptimized.
relname
string | null
​
featuredImageOptimized
object
Show
 
child attributes
​
featuredImageOptimized.
id
string
​
featuredImageOptimized.
imageUrlSource
string | null
​
featuredImageOptimized.
imageUrlOptimized
string | null
​
featuredImageOptimized.
imageSizeKbSource
number | null
​
featuredImageOptimized.
imageSizeKbOptimized
number | null
​
featuredImageOptimized.
imageOptimizedComplete
boolean | null
​
featuredImageOptimized.
imageOptimizedLastUpdated
string | null
​
featuredImageOptimized.
relID
integer | null
​
featuredImageOptimized.
field
string | null
​
featuredImageOptimized.
relname
string | null
​
subEvents
string[] | null
​
markets
object[]
Show
 
child attributes
​
markets.
id
string
​
markets.
question
string | null
​
markets.
conditionId
string
​
markets.
slug
string | null
​
markets.
twitterCardImage
string | null
​
markets.
resolutionSource
string | null
​
markets.
endDate
string<date-time> | null
​
markets.
category
string | null
​
markets.
ammType
string | null
​
markets.
liquidity
string | null
​
markets.
sponsorName
string | null
​
markets.
sponsorImage
string | null
​
markets.
startDate
string<date-time> | null
​
markets.
xAxisValue
string | null
​
markets.
yAxisValue
string | null
​
markets.
denominationToken
string | null
​
markets.
fee
string | null
​
markets.
image
string | null
​
markets.
icon
string | null
​
markets.
lowerBound
string | null
​
markets.
upperBound
string | null
​
markets.
description
string | null
​
markets.
outcomes
string | null
​
markets.
outcomePrices
string | null
​
markets.
volume
string | null
​
markets.
active
boolean | null
​
markets.
marketType
string | null
​
markets.
formatType
string | null
​
markets.
lowerBoundDate
string | null
​
markets.
upperBoundDate
string | null
​
markets.
closed
boolean | null
​
markets.
marketMakerAddress
string
​
markets.
createdBy
integer | null
​
markets.
updatedBy
integer | null
​
markets.
createdAt
string<date-time> | null
​
markets.
updatedAt
string<date-time> | null
​
markets.
closedTime
string | null
​
markets.
wideFormat
boolean | null
​
markets.
new
boolean | null
​
markets.
mailchimpTag
string | null
​
markets.
featured
boolean | null
​
markets.
archived
boolean | null
​
markets.
resolvedBy
string | null
​
markets.
restricted
boolean | null
​
markets.
marketGroup
integer | null
​
markets.
groupItemTitle
string | null
​
markets.
groupItemThreshold
string | null
​
markets.
questionID
string | null
​
markets.
umaEndDate
string | null
​
markets.
enableOrderBook
boolean | null
​
markets.
orderPriceMinTickSize
number | null
​
markets.
orderMinSize
number | null
​
markets.
umaResolutionStatus
string | null
​
markets.
curationOrder
integer | null
​
markets.
volumeNum
number | null
​
markets.
liquidityNum
number | null
​
markets.
endDateIso
string | null
​
markets.
startDateIso
string | null
​
markets.
umaEndDateIso
string | null
​
markets.
hasReviewedDates
boolean | null
​
markets.
readyForCron
boolean | null
​
markets.
commentsEnabled
boolean | null
​
markets.
volume24hr
number | null
​
markets.
volume1wk
number | null
​
markets.
volume1mo
number | null
​
markets.
volume1yr
number | null
​
markets.
gameStartTime
string | null
​
markets.
secondsDelay
integer | null
​
markets.
clobTokenIds
string | null
​
markets.
disqusThread
string | null
​
markets.
shortOutcomes
string | null
​
markets.
teamAID
string | null
​
markets.
teamBID
string | null
​
markets.
umaBond
string | null
​
markets.
umaReward
string | null
​
markets.
fpmmLive
boolean | null
​
markets.
volume24hrAmm
number | null
​
markets.
volume1wkAmm
number | null
​
markets.
volume1moAmm
number | null
​
markets.
volume1yrAmm
number | null
​
markets.
volume24hrClob
number | null
​
markets.
volume1wkClob
number | null
​
markets.
volume1moClob
number | null
​
markets.
volume1yrClob
number | null
​
markets.
volumeAmm
number | null
​
markets.
volumeClob
number | null
​
markets.
liquidityAmm
number | null
​
markets.
liquidityClob
number | null
​
markets.
makerBaseFee
integer | null
​
markets.
takerBaseFee
integer | null
​
markets.
customLiveness
integer | null
​
markets.
acceptingOrders
boolean | null
​
markets.
notificationsEnabled
boolean | null
​
markets.
score
integer | null
​
markets.
imageOptimized
object
Show
 
child attributes
​
markets.imageOptimized.
id
string
​
markets.imageOptimized.
imageUrlSource
string | null
​
markets.imageOptimized.
imageUrlOptimized
string | null
​
markets.imageOptimized.
imageSizeKbSource
number | null
​
markets.imageOptimized.
imageSizeKbOptimized
number | null
​
markets.imageOptimized.
imageOptimizedComplete
boolean | null
​
markets.imageOptimized.
imageOptimizedLastUpdated
string | null
​
markets.imageOptimized.
relID
integer | null
​
markets.imageOptimized.
field
string | null
​
markets.imageOptimized.
relname
string | null
​
markets.
iconOptimized
object
Show
 
child attributes
​
markets.iconOptimized.
id
string
​
markets.iconOptimized.
imageUrlSource
string | null
​
markets.iconOptimized.
imageUrlOptimized
string | null
​
markets.iconOptimized.
imageSizeKbSource
number | null
​
markets.iconOptimized.
imageSizeKbOptimized
number | null
​
markets.iconOptimized.
imageOptimizedComplete
boolean | null
​
markets.iconOptimized.
imageOptimizedLastUpdated
string | null
​
markets.iconOptimized.
relID
integer | null
​
markets.iconOptimized.
field
string | null
​
markets.iconOptimized.
relname
string | null
​
markets.
events
array
​
markets.
categories
object[]
Show
 
child attributes
​
markets.categories.
id
string
​
markets.categories.
label
string | null
​
markets.categories.
parentCategory
string | null
​
markets.categories.
slug
string | null
​
markets.categories.
publishedAt
string | null
​
markets.categories.
createdBy
string | null
​
markets.categories.
updatedBy
string | null
​
markets.categories.
createdAt
string<date-time> | null
​
markets.categories.
updatedAt
string<date-time> | null
​
markets.
tags
object[]
Show
 
child attributes
​
markets.tags.
id
string
​
markets.tags.
label
string | null
​
markets.tags.
slug
string | null
​
markets.tags.
forceShow
boolean | null
​
markets.tags.
publishedAt
string | null
​
markets.tags.
createdBy
integer | null
​
markets.tags.
updatedBy
integer | null
​
markets.tags.
createdAt
string<date-time> | null
​
markets.tags.
updatedAt
string<date-time> | null
​
markets.tags.
forceHide
boolean | null
​
markets.tags.
isCarousel
boolean | null
​
markets.
creator
string | null
​
markets.
ready
boolean | null
​
markets.
funded
boolean | null
​
markets.
pastSlugs
string | null
​
markets.
readyTimestamp
string<date-time> | null
​
markets.
fundedTimestamp
string<date-time> | null
​
markets.
acceptingOrdersTimestamp
string<date-time> | null
​
markets.
competitive
number | null
​
markets.
rewardsMinSize
number | null
​
markets.
rewardsMaxSpread
number | null
​
markets.
spread
number | null
​
markets.
automaticallyResolved
boolean | null
​
markets.
oneDayPriceChange
number | null
​
markets.
oneHourPriceChange
number | null
​
markets.
oneWeekPriceChange
number | null
​
markets.
oneMonthPriceChange
number | null
​
markets.
oneYearPriceChange
number | null
​
markets.
lastTradePrice
number | null
​
markets.
bestBid
number | null
​
markets.
bestAsk
number | null
​
markets.
automaticallyActive
boolean | null
​
markets.
clearBookOnStart
boolean | null
​
markets.
chartColor
string | null
​
markets.
seriesColor
string | null
​
markets.
showGmpSeries
boolean | null
​
markets.
showGmpOutcome
boolean | null
​
markets.
manualActivation
boolean | null
​
markets.
negRiskOther
boolean | null
​
markets.
gameId
string | null
​
markets.
groupItemRange
string | null
​
markets.
sportsMarketType
string | null
​
markets.
line
number | null
​
markets.
umaResolutionStatuses
string | null
​
markets.
pendingDeployment
boolean | null
​
markets.
deploying
boolean | null
​
markets.
deployingTimestamp
string<date-time> | null
​
markets.
scheduledDeploymentTimestamp
string<date-time> | null
​
markets.
rfqEnabled
boolean | null
​
markets.
eventStartTime
string<date-time> | null
​
series
object[]
Show
 
child attributes
​
series.
id
string
​
series.
ticker
string | null
​
series.
slug
string | null
​
series.
title
string | null
​
series.
subtitle
string | null
​
series.
seriesType
string | null
​
series.
recurrence
string | null
​
series.
description
string | null
​
series.
image
string | null
​
series.
icon
string | null
​
series.
layout
string | null
​
series.
active
boolean | null
​
series.
closed
boolean | null
​
series.
archived
boolean | null
​
series.
new
boolean | null
​
series.
featured
boolean | null
​
series.
restricted
boolean | null
​
series.
isTemplate
boolean | null
​
series.
templateVariables
boolean | null
​
series.
publishedAt
string | null
​
series.
createdBy
string | null
​
series.
updatedBy
string | null
​
series.
createdAt
string<date-time> | null
​
series.
updatedAt
string<date-time> | null
​
series.
commentsEnabled
boolean | null
​
series.
competitive
string | null
​
series.
volume24hr
number | null
​
series.
volume
number | null
​
series.
liquidity
number | null
​
series.
startDate
string<date-time> | null
​
series.
pythTokenID
string | null
​
series.
cgAssetName
string | null
​
series.
score
integer | null
​
series.
events
array
​
series.
collections
object[]
Show
 
child attributes
​
series.collections.
id
string
​
series.collections.
ticker
string | null
​
series.collections.
slug
string | null
​
series.collections.
title
string | null
​
series.collections.
subtitle
string | null
​
series.collections.
collectionType
string | null
​
series.collections.
description
string | null
​
series.collections.
tags
string | null
​
series.collections.
image
string | null
​
series.collections.
icon
string | null
​
series.collections.
headerImage
string | null
​
series.collections.
layout
string | null
​
series.collections.
active
boolean | null
​
series.collections.
closed
boolean | null
​
series.collections.
archived
boolean | null
​
series.collections.
new
boolean | null
​
series.collections.
featured
boolean | null
​
series.collections.
restricted
boolean | null
​
series.collections.
isTemplate
boolean | null
​
series.collections.
templateVariables
string | null
​
series.collections.
publishedAt
string | null
​
series.collections.
createdBy
string | null
​
series.collections.
updatedBy
string | null
​
series.collections.
createdAt
string<date-time> | null
​
series.collections.
updatedAt
string<date-time> | null
​
series.collections.
commentsEnabled
boolean | null
​
series.collections.
imageOptimized
object
Show
 
child attributes
​
series.collections.imageOptimized.
id
string
​
series.collections.imageOptimized.
imageUrlSource
string | null
​
series.collections.imageOptimized.
imageUrlOptimized
string | null
​
series.collections.imageOptimized.
imageSizeKbSource
number | null
​
series.collections.imageOptimized.
imageSizeKbOptimized
number | null
​
series.collections.imageOptimized.
imageOptimizedComplete
boolean | null
​
series.collections.imageOptimized.
imageOptimizedLastUpdated
string | null
​
series.collections.imageOptimized.
relID
integer | null
​
series.collections.imageOptimized.
field
string | null
​
series.collections.imageOptimized.
relname
string | null
​
series.collections.
iconOptimized
object
Show
 
child attributes
​
series.collections.iconOptimized.
id
string
​
series.collections.iconOptimized.
imageUrlSource
string | null
​
series.collections.iconOptimized.
imageUrlOptimized
string | null
​
series.collections.iconOptimized.
imageSizeKbSource
number | null
​
series.collections.iconOptimized.
imageSizeKbOptimized
number | null
​
series.collections.iconOptimized.
imageOptimizedComplete
boolean | null
​
series.collections.iconOptimized.
imageOptimizedLastUpdated
string | null
​
series.collections.iconOptimized.
relID
integer | null
​
series.collections.iconOptimized.
field
string | null
​
series.collections.iconOptimized.
relname
string | null
​
series.collections.
headerImageOptimized
object
Show
 
child attributes
​
series.collections.headerImageOptimized.
id
string
​
series.collections.headerImageOptimized.
imageUrlSource
string | null
​
series.collections.headerImageOptimized.
imageUrlOptimized
string | null
​
series.collections.headerImageOptimized.
imageSizeKbSource
number | null
​
series.collections.headerImageOptimized.
imageSizeKbOptimized
number | null
​
series.collections.headerImageOptimized.
imageOptimizedComplete
boolean | null
​
series.collections.headerImageOptimized.
imageOptimizedLastUpdated
string | null
​
series.collections.headerImageOptimized.
relID
integer | null
​
series.collections.headerImageOptimized.
field
string | null
​
series.collections.headerImageOptimized.
relname
string | null
​
series.
categories
object[]
Show
 
child attributes
​
series.categories.
id
string
​
series.categories.
label
string | null
​
series.categories.
parentCategory
string | null
​
series.categories.
slug
string | null
​
series.categories.
publishedAt
string | null
​
series.categories.
createdBy
string | null
​
series.categories.
updatedBy
string | null
​
series.categories.
createdAt
string<date-time> | null
​
series.categories.
updatedAt
string<date-time> | null
​
series.
tags
object[]
Show
 
child attributes
​
series.tags.
id
string
​
series.tags.
label
string | null
​
series.tags.
slug
string | null
​
series.tags.
forceShow
boolean | null
​
series.tags.
publishedAt
string | null
​
series.tags.
createdBy
integer | null
​
series.tags.
updatedBy
integer | null
​
series.tags.
createdAt
string<date-time> | null
​
series.tags.
updatedAt
string<date-time> | null
​
series.tags.
forceHide
boolean | null
​
series.tags.
isCarousel
boolean | null
​
series.
commentCount
integer | null
​
series.
chats
object[]
Show
 
child attributes
​
series.chats.
id
string
​
series.chats.
channelId
string | null
​
series.chats.
channelName
string | null
​
series.chats.
channelImage
string | null
​
series.chats.
live
boolean | null
​
series.chats.
startTime
string<date-time> | null
​
series.chats.
endTime
string<date-time> | null
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
cyom
boolean | null
​
closedTime
string<date-time> | null
​
showAllOutcomes
boolean | null
​
showMarketImages
boolean | null
​
automaticallyResolved
boolean | null
​
enableNegRisk
boolean | null
​
automaticallyActive
boolean | null
​
eventDate
string | null
​
startTime
string<date-time> | null
​
eventWeek
integer | null
​
seriesSlug
string | null
​
score
string | null
​
elapsed
string | null
​
period
string | null
​
live
boolean | null
​
ended
boolean | null
​
finishedTimestamp
string<date-time> | null
​
gmpChartMode
string | null
​
eventCreators
object[]
Show
 
child attributes
​
eventCreators.
id
string
​
eventCreators.
creatorName
string | null
​
eventCreators.
creatorHandle
string | null
​
eventCreators.
creatorUrl
string | null
​
eventCreators.
creatorImage
string | null
​
eventCreators.
createdAt
string<date-time> | null
​
eventCreators.
updatedAt
string<date-time> | null
​
tweetCount
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
​
featuredOrder
integer | null
​
estimateValue
boolean | null
​
cantEstimate
boolean | null
​
estimatedValue
string | null
​
templates
object[]
Show
 
child attributes
​
templates.
id
string
​
templates.
eventTitle
string | null
​
templates.
eventSlug
string | null
​
templates.
eventImage
string | null
​
templates.
marketTitle
string | null
​
templates.
description
string | null
​
templates.
resolutionSource
string | null
​
templates.
negRisk
boolean | null
​
templates.
sortBy
string | null
​
templates.
showMarketImages
boolean | null
​
templates.
seriesSlug
string | null
​
templates.
outcomes
string | null
​
spreadsMainLine
number | null
​
totalsMainLine
number | null
​
carouselMap
string | null
​
pendingDeployment
boolean | null
​
deploying
boolean | null
​
deployingTimestamp
string<date-time> | null
​
scheduledDeploymentTimestamp
string<date-time> | null
​
gameStatus
string | null
Get tags related to a tag slug
Get event by id
⌘
I
github
Powered by Mintlify