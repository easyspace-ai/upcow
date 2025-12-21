# Get Supported Assets - Polymarket Documentation

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
Bridge & Swap
Get Supported Assets
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
Get supported assets
cURL
Copy
Ask AI
curl
 --request
 GET
 \


  --url
 https://bridge.polymarket.com/supported-assets
200
500
Copy
Ask AI
{


  "supportedAssets"
: [


    {


      "chainId"
: 
"1"
,


      "chainName"
: 
"Ethereum"
,


      "token"
: {


        "name"
: 
"USD Coin"
,


        "symbol"
: 
"USDC"
,


        "address"
: 
"0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"
,


        "decimals"
: 
6


      },


      "minCheckoutUsd"
: 
45


    }


  ]


}
Bridge & Swap
Get Supported Assets
Retrieve all supported chains and tokens for deposits and withdrawals.


USDC.e on Polygon:

Polymarket uses USDC.e (Bridged USDC from Ethereum) on Polygon as the native collateral for all markets. When you deposit assets from other chains, they are automatically bridged and swapped to USDC.e on Polygon, which is then used as collateral for trading on Polymarket.


Minimum Deposit Amounts:

Each asset has a 
minCheckoutUsd
 field indicating the minimum deposit amount required in USD. Make sure your deposit meets this minimum to avoid transaction failures.
GET
/
supported-assets
Try it
Get supported assets
cURL
Copy
Ask AI
curl
 --request
 GET
 \


  --url
 https://bridge.polymarket.com/supported-assets
200
500
Copy
Ask AI
{


  "supportedAssets"
: [


    {


      "chainId"
: 
"1"
,


      "chainName"
: 
"Ethereum"
,


      "token"
: {


        "name"
: 
"USD Coin"
,


        "symbol"
: 
"USDC"
,


        "address"
: 
"0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"
,


        "decimals"
: 
6


      },


      "minCheckoutUsd"
: 
45


    }


  ]


}
​
Get Supported Assets


Retrieve all supported chains and tokens for deposits and withdrawals.


​
Response Details


This endpoint returns a list of supported blockchains and their available tokens, including:




Chain IDs and names


Token addresses and symbols


Token decimals and metadata






Note:
 All deposits are converted to USDC.e on Polygon. Use this endpoint to check which source chains and tokens are supported for bridging.


Response
200
application/json
Successfully retrieved supported assets
​
supportedAssets
object[]
List of supported assets with minimum deposit amounts
Show
 
child attributes
​
supportedAssets.
chainId
string
Chain ID
Example
:
"1"
​
supportedAssets.
chainName
string
Human-readable chain name
Example
:
"Ethereum"
​
supportedAssets.
token
object
Show
 
child attributes
​
supportedAssets.token.
name
string
Full token name
Example
:
"USD Coin"
​
supportedAssets.token.
symbol
string
Token symbol
Example
:
"USDC"
​
supportedAssets.token.
address
string
Token contract address
Example
:
"0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"
​
supportedAssets.token.
decimals
integer
Token decimals
Example
:
6
​
supportedAssets.
minCheckoutUsd
number
Minimum deposit amount in USD
Example
:
45
Create Deposit
Overview
⌘
I
github
Powered by Mintlify