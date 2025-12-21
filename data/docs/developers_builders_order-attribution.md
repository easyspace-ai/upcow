# Order Attribution - Polymarket Documentation

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
Polymarket Builders Program
Order Attribution
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
On this page
Overview
Builder API Credentials
Signing Methods
Authentication Headers
Next Steps
Polymarket Builders Program
Order Attribution
Learn how to attribute orders to your builder account
​
Overview


The 
CLOB (Central Limit Order Book)
 is Polymarket’s order matching system. Order attribution adds builder authentication headers when placing orders through the CLOB Client, enabling Polymarket to credit trades to your builder account. This allows you to:




Track volume on the 
Builder Leaderboard


Compete for grants based on trading activity


Monitor performance via the Data API






​
Builder API Credentials


Each builder receives API credentials from their 
Builder Profile
:


Credential
Description
key
Your builder API key identifier
secret
Secret key for signing requests
passphrase
Additional authentication passphrase


Security Notice
: Your Builder API keys must be kept secure. Never expose them in client-side code.




​
Signing Methods


 
Remote Signing (Recommended)
 
Local Signing
Remote signing keeps your credentials secure on a server you control.
How it works:


User signs an order payload


Payload is sent to your builder signing server


Your server adds builder authentication headers


Complete order is sent to the CLOB


​
Server Implementation
Your signing server receives request details and returns the authentication headers. Use the 
buildHmacSignature
 function from the SDK:
TypeScript
Python
Copy
Ask AI
import
 { 


  buildHmacSignature
, 


  BuilderApiKeyCreds
 


} 
from
 "@polymarket/builder-signing-sdk"
;




const
 BUILDER_CREDENTIALS
:
 BuilderApiKeyCreds
 =
 {


  key:
 process
.
env
.
POLY_BUILDER_API_KEY
!
,


  secret:
 process
.
env
.
POLY_BUILDER_SECRET
!
,


  passphrase:
 process
.
env
.
POLY_BUILDER_PASSPHRASE
!
,


};




// POST /sign - receives { method, path, body } from the client SDK


export
 async
 function
 handleSignRequest
(
request
) {


  const
 { 
method
, 
path
, 
body
 } 
=
 await
 request
.
json
();


  


  const
 timestamp
 =
 Date
.
now
().
toString
();


  


  const
 signature
 =
 buildHmacSignature
(


    BUILDER_CREDENTIALS
.
secret
,


    parseInt
(
timestamp
),


    method
,


    path
,


    body


  );




  return
 {


    POLY_BUILDER_SIGNATURE:
 signature
,


    POLY_BUILDER_TIMESTAMP:
 timestamp
,


    POLY_BUILDER_API_KEY:
 BUILDER_CREDENTIALS
.
key
,


    POLY_BUILDER_PASSPHRASE:
 BUILDER_CREDENTIALS
.
passphrase
,


  };


}


Never commit credentials to version control. Use environment variables or a secrets manager.
​
Client Configuration
Point your client to your signing server:
TypeScript
Python
Copy
Ask AI
import
 { 
ClobClient
 } 
from
 "@polymarket/clob-client"
;


import
 { 
BuilderConfig
 } 
from
 "@polymarket/builder-signing-sdk"
;




// Point to your signing server


const
 builderConfig
 =
 new
 BuilderConfig
({


  remoteBuilderConfig:
 { 


    url:
 "https://your-server.com/sign"


  }


});




// Or with optional authorization token


const
 builderConfigWithAuth
 =
 new
 BuilderConfig
({


  remoteBuilderConfig:
 { 


    url:
 "https://your-server.com/sign"
, 


    token:
 "your-auth-token"
 


  }


});




const
 client
 =
 new
 ClobClient
(


  "https://clob.polymarket.com"
,


  137
,


  signer
, 
// ethers v5.x EOA signer


  creds
, 
// User's API Credentials


  2
, 
// signatureType for the Safe proxy wallet


  funderAddress
, 
// Safe proxy wallet address


  undefined
, 


  false
,


  builderConfig


);




// Orders automatically use the signing server


const
 order
 =
 await
 client
.
createOrder
({


  price:
 0.40
,


  side:
 Side
.
BUY
,


  size:
 5
,


  tokenID:
 "YOUR_TOKEN_ID"


});




const
 response
 =
 await
 client
.
postOrder
(
order
);


​
Troubleshooting
Invalid Signature Errors
Error:
 Client receives invalid signature errors
Solution:


Verify the request body is passed correctly as JSON


Check that 
path
, 
body
, and 
method
 match what the client sends


Ensure your server and client use the same Builder API credentials


Missing Credentials
Error:
 
Builder credentials not configured
 or undefined values
Solution:
 Ensure your environment variables are set:


POLY_BUILDER_API_KEY


POLY_BUILDER_SECRET


POLY_BUILDER_PASSPHRASE


Sign orders locally when you control the entire order placement flow.
How it works:


Your system creates and signs orders on behalf of users


Your system uses Builder API credentials locally to add headers


Complete signed order is sent directly to the CLOB


TypeScript
Python
Copy
Ask AI
import
 { 
ClobClient
 } 
from
 "@polymarket/clob-client"
;


import
 { 
BuilderConfig
, 
BuilderApiKeyCreds
 } 
from
 "@polymarket/builder-signing-sdk"
;




// Configure with local builder credentials


const
 builderCreds
:
 BuilderApiKeyCreds
 =
 {


  key:
 process
.
env
.
POLY_BUILDER_API_KEY
!
,


  secret:
 process
.
env
.
POLY_BUILDER_SECRET
!
,


  passphrase:
 process
.
env
.
POLY_BUILDER_PASSPHRASE
!


};




const
 builderConfig
 =
 new
 BuilderConfig
({


  localBuilderCreds:
 builderCreds


});




const
 client
 =
 new
 ClobClient
(


  "https://clob.polymarket.com"
,


  137
,


  signer
, 
// ethers v5.x EOA signer


  creds
, 
// User's API Credentials


  2
, 
// signatureType for the Safe proxy wallet


  funderAddress
, 
// Safe proxy wallet address


  undefined
, 


  false
,


  builderConfig


);




// Orders automatically include builder headers


const
 order
 =
 await
 client
.
createOrder
({


  price:
 0.40
,


  side:
 Side
.
BUY
,


  size:
 5
,


  tokenID:
 "YOUR_TOKEN_ID"


});




const
 response
 =
 await
 client
.
postOrder
(
order
);


Never commit credentials to version control. Use environment variables or a secrets manager.




​
Authentication Headers


The SDK automatically generates and attaches these headers to each request:


Header
Description
POLY_BUILDER_API_KEY
Your builder API key
POLY_BUILDER_TIMESTAMP
Unix timestamp of signature creation
POLY_BUILDER_PASSPHRASE
Your builder passphrase
POLY_BUILDER_SIGNATURE
HMAC signature of the request


With 
local signing
, the SDK constructs and attaches these headers automatically. With 
remote signing
, your server must return these headers (see Server Implementation above), and the SDK attaches them to the request.




​
Next Steps


Relayer Client
Learn how to configure and use the Relay Client too!
CLOB Client Methods
Explore the complete CLOB client reference
Builder Profile & Keys
Relayer Client
⌘
I
github
Powered by Mintlify