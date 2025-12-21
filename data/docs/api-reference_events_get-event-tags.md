# Get event tags - Polymarket Documentation

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
Get event tags
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
Get event tags
cURL
Copy
Ask AI
curl
 --request
 GET
 \


  --url
 https://gamma-api.polymarket.com/events/{id}/tags
200
Copy
Ask AI
[


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


]
Events
Get event tags
GET
/
events
/
{id}
/
tags
Try it
Get event tags
cURL
Copy
Ask AI
curl
 --request
 GET
 \


  --url
 https://gamma-api.polymarket.com/events/{id}/tags
200
Copy
Ask AI
[


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


]
Path Parameters
​
id
integer
required
Response
200
application/json
Tags attached to the event
​
id
string
​
label
string | null
​
slug
string | null
​
forceShow
boolean | null
​
publishedAt
string | null
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
forceHide
boolean | null
​
isCarousel
boolean | null
Get event by id
Get event by slug
⌘
I
github
Powered by Mintlify