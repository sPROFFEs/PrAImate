package com.praimate.mobile;

import org.junit.Test;
import static org.junit.Assert.*;

public class EndpointTest {
    @Test public void accessCallbacksCompareActualHttpsOrigins(){assertTrue(Endpoint.sameHTTPSOrigin("https://server.example:443","https://SERVER.example/v1/status"));for(String callback:new String[]{"http://server.example/v1/status","https://server.example:444/v1/status","https://server.example.attacker.test/v1/status","https://user@server.example/v1/status"})assertFalse(Endpoint.sameHTTPSOrigin("https://server.example",callback));}
    @Test public void normalizesServerOrigins(){assertEquals("https://server.example:8844",Endpoint.normalize("https://server.example:8844/",false));assertEquals("http://127.0.0.1:8844",Endpoint.normalize("http://127.0.0.1:8844",false));}
    @Test public void rejectsCredentialsPathsAndPlaintextRemoteHosts(){for(String input:new String[]{"http://server.example","https://user:password@server.example","https://server.example/v1","https://server.example?token=x","file:///tmp/local","https://server.example#fragment","http://10.0.2.2:8844"}){try{Endpoint.normalize(input,false);fail(input);}catch(IllegalArgumentException expected){}}}
    @Test public void debugEmulatorCanUseItsHostAlias(){assertEquals("http://10.0.2.2:8844",Endpoint.normalize("http://10.0.2.2:8844",true));}
    @Test public void requestsStayOnTheSelectedServer(){assertEquals("/v1/call",Endpoint.apiPath(Endpoint.APP_ORIGIN+"/v1/call"));assertEquals("/v1/download?path=hello%20world",Endpoint.apiPath("/v1/download?path=hello%20world"));for(String input:new String[]{"https://attacker.example/v1/status","//attacker.example/v1/status","/v1/../secret","/v1/%2e%2e/secret","/assets/index.html"}){try{Endpoint.apiPath(input);fail(input);}catch(IllegalArgumentException expected){}}}
    @Test public void navigationCannotInjectAnotherOrigin(){assertEquals("#page=workers&worker=run1",Endpoint.route("#page=workers&worker=run1"));try{Endpoint.route("https://attacker.example");fail();}catch(IllegalArgumentException expected){}}
}
