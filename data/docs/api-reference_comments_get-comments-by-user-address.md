# Get comments by user address - Polymarket Documentation

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
Comments
Get comments by user address
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
Comments
GET
List comments
GET
Get comments by comment id
GET
Get comments by user address
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
Get comments by user address
cURL
Copy
Ask AI
curl
 --request
 GET
 \


  --url
 https://gamma-api.polymarket.com/comments/user_address/{user_address}
200
Copy
Ask AI
[


  {


    "id"
: 
"<string>"
,


    "body"
: 
"<string>"
,


    "parentEntityType"
: 
"<string>"
,


    "parentEntityID"
: 
123
,


    "parentCommentID"
: 
"<string>"
,


    "userAddress"
: 
"<string>"
,


    "replyAddress"
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


    "profile"
: {


      "name"
: 
"<string>"
,


      "pseudonym"
: 
"<string>"
,


      "displayUsernamePublic"
: 
true
,


      "bio"
: 
"<string>"
,


      "isMod"
: 
true
,


      "isCreator"
: 
true
,


      "proxyWallet"
: 
"<string>"
,


      "baseAddress"
: 
"<string>"
,


      "profileImage"
: 
"<string>"
,


      "profileImageOptimized"
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


      "positions"
: [


        {


          "tokenId"
: 
"<string>"
,


          "positionSize"
: 
"<string>"


        }


      ]


    },


    "reactions"
: [


      {


        "id"
: 
"<string>"
,


        "commentID"
: 
123
,


        "reactionType"
: 
"<string>"
,


        "icon"
: 
"<string>"
,


        "userAddress"
: 
"<string>"
,


        "createdAt"
: 
"2023-11-07T05:31:56Z"
,


        "profile"
: {


          "name"
: 
"<string>"
,


          "pseudonym"
: 
"<string>"
,


          "displayUsernamePublic"
: 
true
,


          "bio"
: 
"<string>"
,


          "isMod"
: 
true
,


          "isCreator"
: 
true
,


          "proxyWallet"
: 
"<string>"
,


          "baseAddress"
: 
"<string>"
,


          "profileImage"
: 
"<string>"
,


          "profileImageOptimized"
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


          "positions"
: [


            {


              "tokenId"
: 
"<string>"
,


              "positionSize"
: 
"<string>"


            }


          ]


        }


      }


    ],


    "reportCount"
: 
123
,


    "reactionCount"
: 
123


  }


]
Comments
Get comments by user address
GET
/
comments
/
user_address
/
{user_address}
Try it
Get comments by user address
cURL
Copy
Ask AI
curl
 --request
 GET
 \


  --url
 https://gamma-api.polymarket.com/comments/user_address/{user_address}
200
Copy
Ask AI
[


  {


    "id"
: 
"<string>"
,


    "body"
: 
"<string>"
,


    "parentEntityType"
: 
"<string>"
,


    "parentEntityID"
: 
123
,


    "parentCommentID"
: 
"<string>"
,


    "userAddress"
: 
"<string>"
,


    "replyAddress"
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


    "profile"
: {


      "name"
: 
"<string>"
,


      "pseudonym"
: 
"<string>"
,


      "displayUsernamePublic"
: 
true
,


      "bio"
: 
"<string>"
,


      "isMod"
: 
true
,


      "isCreator"
: 
true
,


      "proxyWallet"
: 
"<string>"
,


      "baseAddress"
: 
"<string>"
,


      "profileImage"
: 
"<string>"
,


      "profileImageOptimized"
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


      "positions"
: [


        {


          "tokenId"
: 
"<string>"
,


          "positionSize"
: 
"<string>"


        }


      ]


    },


    "reactions"
: [


      {


        "id"
: 
"<string>"
,


        "commentID"
: 
123
,


        "reactionType"
: 
"<string>"
,


        "icon"
: 
"<string>"
,


        "userAddress"
: 
"<string>"
,


        "createdAt"
: 
"2023-11-07T05:31:56Z"
,


        "profile"
: {


          "name"
: 
"<string>"
,


          "pseudonym"
: 
"<string>"
,


          "displayUsernamePublic"
: 
true
,


          "bio"
: 
"<string>"
,


          "isMod"
: 
true
,


          "isCreator"
: 
true
,


          "proxyWallet"
: 
"<string>"
,


          "baseAddress"
: 
"<string>"
,


          "profileImage"
: 
"<string>"
,


          "profileImageOptimized"
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


          "positions"
: [


            {


              "tokenId"
: 
"<string>"
,


              "positionSize"
: 
"<string>"


            }


          ]


        }


      }


    ],


    "reportCount"
: 
123
,


    "reactionCount"
: 
123


  }


]
Path Parameters
​
user_address
string
required
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
Response
200 - application/json
Comments
​
id
string
​
body
string | null
​
parentEntityType
string | null
​
parentEntityID
integer | null
​
parentCommentID
string | null
​
userAddress
string | null
​
replyAddress
string | null
​
createdAt
string<date-time> | null
​
updatedAt
string<date-time> | null
​
profile
object
Show
 
child attributes
​
profile.
name
string | null
​
profile.
pseudonym
string | null
​
profile.
displayUsernamePublic
boolean | null
​
profile.
bio
string | null
​
profile.
isMod
boolean | null
​
profile.
isCreator
boolean | null
​
profile.
proxyWallet
string | null
​
profile.
baseAddress
string | null
​
profile.
profileImage
string | null
​
profile.
profileImageOptimized
object
Show
 
child attributes
​
profile.profileImageOptimized.
id
string
​
profile.profileImageOptimized.
imageUrlSource
string | null
​
profile.profileImageOptimized.
imageUrlOptimized
string | null
​
profile.profileImageOptimized.
imageSizeKbSource
number | null
​
profile.profileImageOptimized.
imageSizeKbOptimized
number | null
​
profile.profileImageOptimized.
imageOptimizedComplete
boolean | null
​
profile.profileImageOptimized.
imageOptimizedLastUpdated
string | null
​
profile.profileImageOptimized.
relID
integer | null
​
profile.profileImageOptimized.
field
string | null
​
profile.profileImageOptimized.
relname
string | null
​
profile.
positions
object[]
Show
 
child attributes
​
profile.positions.
tokenId
string | null
​
profile.positions.
positionSize
string | null
​
reactions
object[]
Show
 
child attributes
​
reactions.
id
string
​
reactions.
commentID
integer | null
​
reactions.
reactionType
string | null
​
reactions.
icon
string | null
​
reactions.
userAddress
string | null
​
reactions.
createdAt
string<date-time> | null
​
reactions.
profile
object
Show
 
child attributes
​
reactions.profile.
name
string | null
​
reactions.profile.
pseudonym
string | null
​
reactions.profile.
displayUsernamePublic
boolean | null
​
reactions.profile.
bio
string | null
​
reactions.profile.
isMod
boolean | null
​
reactions.profile.
isCreator
boolean | null
​
reactions.profile.
proxyWallet
string | null
​
reactions.profile.
baseAddress
string | null
​
reactions.profile.
profileImage
string | null
​
reactions.profile.
profileImageOptimized
object
Show
 
child attributes
​
reactions.profile.profileImageOptimized.
id
string
​
reactions.profile.profileImageOptimized.
imageUrlSource
string | null
​
reactions.profile.profileImageOptimized.
imageUrlOptimized
string | null
​
reactions.profile.profileImageOptimized.
imageSizeKbSource
number | null
​
reactions.profile.profileImageOptimized.
imageSizeKbOptimized
number | null
​
reactions.profile.profileImageOptimized.
imageOptimizedComplete
boolean | null
​
reactions.profile.profileImageOptimized.
imageOptimizedLastUpdated
string | null
​
reactions.profile.profileImageOptimized.
relID
integer | null
​
reactions.profile.profileImageOptimized.
field
string | null
​
reactions.profile.profileImageOptimized.
relname
string | null
​
reactions.profile.
positions
object[]
Show
 
child attributes
​
reactions.profile.positions.
tokenId
string | null
​
reactions.profile.positions.
positionSize
string | null
​
reportCount
integer | null
​
reactionCount
integer | null
Get comments by comment id
Search markets, events, and profiles
⌘
I
github
Powered by Mintlify