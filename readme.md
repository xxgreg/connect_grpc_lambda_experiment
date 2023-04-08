# Connect GRPC Service + AWS Lambda

An of example of how run a connect go GRPC Service on AWS Lambda. Only unary 
connect protocol requests are supported.

In theory binary protocol should work, but I haven't tested it.

To configure this you need to create an Rest API Gateway. Set up the root 
resource as proxy and use "Lambda Proxy integration". 

API Gateway handles an incoming HTTP request and translates it to a JSON message,
which is used to invoke the Lambda. The Lambda translates the JSON message back
into a Go net/http HttpRequest object. This HTTP request is given to the
connect HTTP handler, and the response is buffered, and converted into the 
JSON format expected by API Gateway Lambda Proxy integration.

Since the connect GRPC protocol is simple enough, this should just work™ for 
unary requests. Well at least that's the theory.
