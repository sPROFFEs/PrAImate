package com.praimate.mobile;

import org.junit.Test;
import static org.junit.Assert.*;

public class AccessSessionTest {
    @Test public void onlyApplicationAccessCookiesAreTransferred(){
        assertEquals("CF_Authorization=app.jwt; CF_Binding=bound; CF_AppSession=csrf",AccessSession.cookies("tracking=no; CF_Authorization=app.jwt; CF_Binding=bound; CF_AppSession=csrf; CF_Session=team-only"));
        assertEquals("",AccessSession.cookies("tracking=no"));
    }
    @Test public void malformedCookiesNeverBecomeHeaders(){
        for(String value:new String[]{"CF_Authorization=x\r\nAuthorization: Bearer stolen","CF_Authorization=a; CF_Authorization=b"}){
            try{AccessSession.cookies(value);fail();}catch(IllegalArgumentException expected){}
        }
    }
    @Test public void recognizesAccessWithoutTreatingApp401AsAccess(){
        assertTrue(AccessSession.challenge(302,"https://team.cloudflareaccess.com/cdn-cgi/access/login/app?token=secret","text/html",false));
        assertTrue(AccessSession.challenge(302,"/cdn-cgi/access/login/app","",false));
        assertTrue(AccessSession.challenge(403,"","text/html",true));
        assertFalse(AccessSession.challenge(401,"","application/json",true));
        assertFalse(AccessSession.challenge(302,"https://elsewhere.example/login","text/html",false));
    }
}
