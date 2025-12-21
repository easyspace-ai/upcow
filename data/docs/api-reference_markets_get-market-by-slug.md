# Get market by slug - Polymarket Documentation

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
Markets
Get market by slug
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
GET
List markets
GET
Get market by id
GET
Get market tags by id
GET
Get market by slug
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
Get market by slug
cURL
Copy
Ask AI
curl
 --request
 GET
 \


  --url
 https://gamma-api.polymarket.com/markets/slug/{slug}
200
Copy
Ask AI
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
: 
"<array>"
,


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
Markets
Get market by slug
GET
/
markets
/
slug
/
{slug}
Try it
Get market by slug
cURL
Copy
Ask AI
curl
 --request
 GET
 \


  --url
 https://gamma-api.polymarket.com/markets/slug/{slug}
200
Copy
Ask AI
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
: 
"<array>"
,


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
Path Parameters
​
slug
string
required
Query Parameters
​
include_tag
boolean
Response
200
application/json
Market
​
id
string
​
question
string | null
​
conditionId
string
​
slug
string | null
​
twitterCardImage
string | null
​
resolutionSource
string | null
​
endDate
string<date-time> | null
​
category
string | null
​
ammType
string | null
​
liquidity
string | null
​
sponsorName
string | null
​
sponsorImage
string | null
​
startDate
string<date-time> | null
​
xAxisValue
string | null
​
yAxisValue
string | null
​
denominationToken
string | null
​
fee
string | null
​
image
string | null
​
icon
string | null
​
lowerBound
string | null
​
upperBound
string | null
​
description
string | null
​
outcomes
string | null
​
outcomePrices
string | null
​
volume
string | null
​
active
boolean | null
​
marketType
string | null
​
formatType
string | null
​
lowerBoundDate
string | null
​
upperBoundDate
string | null
​
closed
boolean | null
​
marketMakerAddress
string
​
createdBy
integer | null
​
updatedBy
integer | null
​
createdAt
string<date-time> | null
​
updatedAt
string<date-time> | null
​
closedTime
string | null
​
wideFormat
boolean | null
​
new
boolean | null
​
mailchimpTag
string | null
​
featured
boolean | null
​
archived
boolean | null
​
resolvedBy
string | null
​
restricted
boolean | null
​
marketGroup
integer | null
​
groupItemTitle
string | null
​
groupItemThreshold
string | null
​
questionID
string | null
​
umaEndDate
string | null
​
enableOrderBook
boolean | null
​
orderPriceMinTickSize
number | null
​
orderMinSize
number | null
​
umaResolutionStatus
string | null
​
curationOrder
integer | null
​
volumeNum
number | null
​
liquidityNum
number | null
​
endDateIso
string | null
​
startDateIso
string | null
​
umaEndDateIso
string | null
​
hasReviewedDates
boolean | null
​
readyForCron
boolean | null
​
commentsEnabled
boolean | null
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
gameStartTime
string | null
​
secondsDelay
integer | null
​
clobTokenIds
string | null
​
disqusThread
string | null
​
shortOutcomes
string | null
​
teamAID
string | null
​
teamBID
string | null
​
umaBond
string | null
​
umaReward
string | null
​
fpmmLive
boolean | null
​
volume24hrAmm
number | null
​
volume1wkAmm
number | null
​
volume1moAmm
number | null
​
volume1yrAmm
number | null
​
volume24hrClob
number | null
​
volume1wkClob
number | null
​
volume1moClob
number | null
​
volume1yrClob
number | null
​
volumeAmm
number | null
​
volumeClob
number | null
​
liquidityAmm
number | null
​
liquidityClob
number | null
​
makerBaseFee
integer | null
​
takerBaseFee
integer | null
​
customLiveness
integer | null
​
acceptingOrders
boolean | null
​
notificationsEnabled
boolean | null
​
score
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
array
​
events.
series
object[]
Show
 
child attributes
​
events.series.
id
string
​
events.series.
ticker
string | null
​
events.series.
slug
string | null
​
events.series.
title
string | null
​
events.series.
subtitle
string | null
​
events.series.
seriesType
string | null
​
events.series.
recurrence
string | null
​
events.series.
description
string | null
​
events.series.
image
string | null
​
events.series.
icon
string | null
​
events.series.
layout
string | null
​
events.series.
active
boolean | null
​
events.series.
closed
boolean | null
​
events.series.
archived
boolean | null
​
events.series.
new
boolean | null
​
events.series.
featured
boolean | null
​
events.series.
restricted
boolean | null
​
events.series.
isTemplate
boolean | null
​
events.series.
templateVariables
boolean | null
​
events.series.
publishedAt
string | null
​
events.series.
createdBy
string | null
​
events.series.
updatedBy
string | null
​
events.series.
createdAt
string<date-time> | null
​
events.series.
updatedAt
string<date-time> | null
​
events.series.
commentsEnabled
boolean | null
​
events.series.
competitive
string | null
​
events.series.
volume24hr
number | null
​
events.series.
volume
number | null
​
events.series.
liquidity
number | null
​
events.series.
startDate
string<date-time> | null
​
events.series.
pythTokenID
string | null
​
events.series.
cgAssetName
string | null
​
events.series.
score
integer | null
​
events.series.
events
array
​
events.series.
collections
object[]
Show
 
child attributes
​
events.series.collections.
id
string
​
events.series.collections.
ticker
string | null
​
events.series.collections.
slug
string | null
​
events.series.collections.
title
string | null
​
events.series.collections.
subtitle
string | null
​
events.series.collections.
collectionType
string | null
​
events.series.collections.
description
string | null
​
events.series.collections.
tags
string | null
​
events.series.collections.
image
string | null
​
events.series.collections.
icon
string | null
​
events.series.collections.
headerImage
string | null
​
events.series.collections.
layout
string | null
​
events.series.collections.
active
boolean | null
​
events.series.collections.
closed
boolean | null
​
events.series.collections.
archived
boolean | null
​
events.series.collections.
new
boolean | null
​
events.series.collections.
featured
boolean | null
​
events.series.collections.
restricted
boolean | null
​
events.series.collections.
isTemplate
boolean | null
​
events.series.collections.
templateVariables
string | null
​
events.series.collections.
publishedAt
string | null
​
events.series.collections.
createdBy
string | null
​
events.series.collections.
updatedBy
string | null
​
events.series.collections.
createdAt
string<date-time> | null
​
events.series.collections.
updatedAt
string<date-time> | null
​
events.series.collections.
commentsEnabled
boolean | null
​
events.series.collections.
imageOptimized
object
Show
 
child attributes
​
events.series.collections.imageOptimized.
id
string
​
events.series.collections.imageOptimized.
imageUrlSource
string | null
​
events.series.collections.imageOptimized.
imageUrlOptimized
string | null
​
events.series.collections.imageOptimized.
imageSizeKbSource
number | null
​
events.series.collections.imageOptimized.
imageSizeKbOptimized
number | null
​
events.series.collections.imageOptimized.
imageOptimizedComplete
boolean | null
​
events.series.collections.imageOptimized.
imageOptimizedLastUpdated
string | null
​
events.series.collections.imageOptimized.
relID
integer | null
​
events.series.collections.imageOptimized.
field
string | null
​
events.series.collections.imageOptimized.
relname
string | null
​
events.series.collections.
iconOptimized
object
Show
 
child attributes
​
events.series.collections.iconOptimized.
id
string
​
events.series.collections.iconOptimized.
imageUrlSource
string | null
​
events.series.collections.iconOptimized.
imageUrlOptimized
string | null
​
events.series.collections.iconOptimized.
imageSizeKbSource
number | null
​
events.series.collections.iconOptimized.
imageSizeKbOptimized
number | null
​
events.series.collections.iconOptimized.
imageOptimizedComplete
boolean | null
​
events.series.collections.iconOptimized.
imageOptimizedLastUpdated
string | null
​
events.series.collections.iconOptimized.
relID
integer | null
​
events.series.collections.iconOptimized.
field
string | null
​
events.series.collections.iconOptimized.
relname
string | null
​
events.series.collections.
headerImageOptimized
object
Show
 
child attributes
​
events.series.collections.headerImageOptimized.
id
string
​
events.series.collections.headerImageOptimized.
imageUrlSource
string | null
​
events.series.collections.headerImageOptimized.
imageUrlOptimized
string | null
​
events.series.collections.headerImageOptimized.
imageSizeKbSource
number | null
​
events.series.collections.headerImageOptimized.
imageSizeKbOptimized
number | null
​
events.series.collections.headerImageOptimized.
imageOptimizedComplete
boolean | null
​
events.series.collections.headerImageOptimized.
imageOptimizedLastUpdated
string | null
​
events.series.collections.headerImageOptimized.
relID
integer | null
​
events.series.collections.headerImageOptimized.
field
string | null
​
events.series.collections.headerImageOptimized.
relname
string | null
​
events.series.
categories
object[]
Show
 
child attributes
​
events.series.categories.
id
string
​
events.series.categories.
label
string | null
​
events.series.categories.
parentCategory
string | null
​
events.series.categories.
slug
string | null
​
events.series.categories.
publishedAt
string | null
​
events.series.categories.
createdBy
string | null
​
events.series.categories.
updatedBy
string | null
​
events.series.categories.
createdAt
string<date-time> | null
​
events.series.categories.
updatedAt
string<date-time> | null
​
events.series.
tags
object[]
Show
 
child attributes
​
events.series.tags.
id
string
​
events.series.tags.
label
string | null
​
events.series.tags.
slug
string | null
​
events.series.tags.
forceShow
boolean | null
​
events.series.tags.
publishedAt
string | null
​
events.series.tags.
createdBy
integer | null
​
events.series.tags.
updatedBy
integer | null
​
events.series.tags.
createdAt
string<date-time> | null
​
events.series.tags.
updatedAt
string<date-time> | null
​
events.series.tags.
forceHide
boolean | null
​
events.series.tags.
isCarousel
boolean | null
​
events.series.
commentCount
integer | null
​
events.series.
chats
object[]
Show
 
child attributes
​
events.series.chats.
id
string
​
events.series.chats.
channelId
string | null
​
events.series.chats.
channelName
string | null
​
events.series.chats.
channelImage
string | null
​
events.series.chats.
live
boolean | null
​
events.series.chats.
startTime
string<date-time> | null
​
events.series.chats.
endTime
string<date-time> | null
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
creator
string | null
​
ready
boolean | null
​
funded
boolean | null
​
pastSlugs
string | null
​
readyTimestamp
string<date-time> | null
​
fundedTimestamp
string<date-time> | null
​
acceptingOrdersTimestamp
string<date-time> | null
​
competitive
number | null
​
rewardsMinSize
number | null
​
rewardsMaxSpread
number | null
​
spread
number | null
​
automaticallyResolved
boolean | null
​
oneDayPriceChange
number | null
​
oneHourPriceChange
number | null
​
oneWeekPriceChange
number | null
​
oneMonthPriceChange
number | null
​
oneYearPriceChange
number | null
​
lastTradePrice
number | null
​
bestBid
number | null
​
bestAsk
number | null
​
automaticallyActive
boolean | null
​
clearBookOnStart
boolean | null
​
chartColor
string | null
​
seriesColor
string | null
​
showGmpSeries
boolean | null
​
showGmpOutcome
boolean | null
​
manualActivation
boolean | null
​
negRiskOther
boolean | null
​
gameId
string | null
​
groupItemRange
string | null
​
sportsMarketType
string | null
​
line
number | null
​
umaResolutionStatuses
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
rfqEnabled
boolean | null
​
eventStartTime
string<date-time> | null
Get market tags by id
List series
⌘
I
github
Powered by Mintlify