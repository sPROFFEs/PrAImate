package com.praimate.mobile;
import org.junit.Test;
import static org.junit.Assert.*;
public class UpdatePolicyTest {
    @Test public void comparesMobileAndLegacyServerVersions(){assertTrue(UpdatePolicy.newer("v1.2.27","1.2.26-server-debug"));assertFalse(UpdatePolicy.newer("v1.2.27","1.2.27-debug"));assertTrue(UpdatePolicy.newer("server-v1.3.0","1.2.99"));assertFalse(UpdatePolicy.newer("v1.2.9","1.2.27-debug"));}
    @Test public void neverSendsUpdateRequestsToTheSelectedServer(){
        for(String bad:new String[]{"http://github.com/sPROFFEs/praimate/releases/latest","https://github.com.attacker.test/sPROFFEs/praimate/releases/latest","https://user:secret@github.com/sPROFFEs/praimate/releases/latest","https://github.com/attacker/repo/releases/latest","https://server.example/v1/status","https://github.com:444/sPROFFEs/praimate/releases/latest"}){try{UpdatePolicy.trustedURL(bad);fail(bad);}catch(IllegalArgumentException expected){}}
        UpdatePolicy.trustedURL(UpdatePolicy.API);UpdatePolicy.trustedURL(UpdatePolicy.releaseURL("v1.2.27","praimate-mobile-preview.apk"));UpdatePolicy.trustedURL("https://release-assets.githubusercontent.com/path?signature=fixture");
    }
    @Test public void requiresAnExactUniqueChecksum(){String hash="a".repeat(64);assertEquals(hash,UpdatePolicy.checksum(hash+"  praimate-mobile-preview.apk\n","praimate-mobile-preview.apk"));for(String invalid:new String[]{hash+"  other.apk","bad  praimate-mobile-preview.apk",hash+"  praimate-mobile-preview.apk\n"+hash+"  praimate-mobile-preview.apk"}){try{UpdatePolicy.checksum(invalid,"praimate-mobile-preview.apk");fail();}catch(IllegalArgumentException expected){}}}
    @Test public void rejectsInjectedReleasePaths(){try{UpdatePolicy.releaseURL("v1.2.27/../../other","app.apk");fail();}catch(IllegalArgumentException expected){}}
}
