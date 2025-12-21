# Get top holders for markets - Polymarket Documentation

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
Core
Get top holders for markets
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
Search
Data-API
Health
Core
GET
Get current positions for a user
GET
Get trades for a user or markets
GET
Get user activity
GET
Get top holders for markets
GET
Get total value of a user's positions
GET
Get closed positions for a user
GET
Get trader leaderboard rankings
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
Get top holders for markets
cURL
Copy
Ask AI
curl
 --request
 GET
 \


  --url
 https://data-api.polymarket.com/holders
200
400
401
500
Copy
Ask AI
[


  {


    "token"
: 
"<string>"
,


    "holders"
: [


      {


        "proxyWallet"
: 
"0x56687bf447db6ffa42ffe2204a05edaa20f55839"
,


        "bio"
: 
"<string>"
,


        "asset"
: 
"<string>"
,


        "pseudonym"
: 
"<string>"
,


        "amount"
: 
123
,


        "displayUsernamePublic"
: 
true
,


        "outcomeIndex"
: 
123
,


        "name"
: 
"<string>"
,


        "profileImage"
: 
"<string>"
,


        "profileImageOptimized"
: 
"<string>"


      }


    ]


  }


]
Core
Get top holders for markets
GET
/
holders
Try it
Get top holders for markets
cURL
Copy
Ask AI
curl
 --request
 GET
 \


  --url
 https://data-api.polymarket.com/holders
200
400
401
500
Copy
Ask AI
[


  {


    "token"
: 
"<string>"
,


    "holders"
: [


      {


        "proxyWallet"
: 
"0x56687bf447db6ffa42ffe2204a05edaa20f55839"
,


        "bio"
: 
"<string>"
,


        "asset"
: 
"<string>"
,


        "pseudonym"
: 
"<string>"
,


        "amount"
: 
123
,


        "displayUsernamePublic"
: 
true
,


        "outcomeIndex"
: 
123
,


        "name"
: 
"<string>"
,


        "profileImage"
: 
"<string>"
,


        "profileImageOptimized"
: 
"<string>"


      }


    ]


  }


]
Query Parameters
​
limit
integer
default:
20
Maximum number of holders to return per token. Capped at 20.
Required range
: 
0 <= x <= 20
​
market
string[]
required
Comma-separated list of condition IDs.
0x-prefixed 64-hex string
​
minBalance
integer
default:
1
Required range
: 
0 <= x <= 999999
Response
200
application/json
Success
​
token
string
​
holders
object[]
Show
 
child attributes
​
holders.
proxyWallet
string
User Profile Address (0x-prefixed, 40 hex chars)
Example
:
"0x56687bf447db6ffa42ffe2204a05edaa20f55839"
​
holders.
bio
string
​
holders.
asset
string
​
holders.
pseudonym
string
​
holders.
amount
number
​
holders.
displayUsernamePublic
boolean
​
holders.
outcomeIndex
integer
​
holders.
name
string
​
holders.
profileImage
string
​
holders.
profileImageOptimized
string
Get user activity
Get total value of a user's positions
⌘
I
github
Powered by Mintlify