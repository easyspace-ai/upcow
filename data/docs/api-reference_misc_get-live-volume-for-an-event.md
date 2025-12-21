# Get live volume for an event - Polymarket Documentation

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
Misc
Get live volume for an event
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
Misc
GET
Get total markets a user has traded
GET
Get open interest
GET
Get live volume for an event
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
Get live volume for an event
cURL
Copy
Ask AI
curl
 --request
 GET
 \


  --url
 https://data-api.polymarket.com/live-volume
200
400
500
Copy
Ask AI
[


  {


    "total"
: 
123
,


    "markets"
: [


      {


        "market"
: 
"0xdd22472e552920b8438158ea7238bfadfa4f736aa4cee91a6b86c39ead110917"
,


        "value"
: 
123


      }


    ]


  }


]
Misc
Get live volume for an event
GET
/
live-volume
Try it
Get live volume for an event
cURL
Copy
Ask AI
curl
 --request
 GET
 \


  --url
 https://data-api.polymarket.com/live-volume
200
400
500
Copy
Ask AI
[


  {


    "total"
: 
123
,


    "markets"
: [


      {


        "market"
: 
"0xdd22472e552920b8438158ea7238bfadfa4f736aa4cee91a6b86c39ead110917"
,


        "value"
: 
123


      }


    ]


  }


]
Query Parameters
​
id
integer
required
Required range
: 
x >= 1
Response
200
application/json
Success
​
total
number
​
markets
object[]
Show
 
child attributes
​
markets.
market
string
0x-prefixed 64-hex string
Example
:
"0xdd22472e552920b8438158ea7238bfadfa4f736aa4cee91a6b86c39ead110917"
​
markets.
value
number
Get open interest
Get aggregated builder leaderboard
⌘
I
github
Powered by Mintlify