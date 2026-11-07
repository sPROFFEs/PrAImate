package com.praimate.mobile;

import androidx.test.ext.junit.runners.AndroidJUnit4;
import androidx.test.platform.app.InstrumentationRegistry;
import android.content.Context;
import org.junit.Test;
import org.junit.runner.RunWith;
import static org.junit.Assert.*;

@RunWith(AndroidJUnit4.class)
public class VaultTest {
    @Test public void accessSessionIsEncryptedScopedAndCannotUndoForget()throws Exception{
        Context context=InstrumentationRegistry.getInstrumentation().getTargetContext();Vault vault=new Vault(context);
        String id=vault.add("Access fixture","https://access.example"),other=vault.add("Another profile","https://access.example");
        try{
            long started=vault.profile(id).optLong("accessVersion");
            vault.finishAccess(id,"https://access.example",started,"CF_Authorization=synthetic.access.jwt; CF_Binding=bound","fixture-agent");
            assertEquals("CF_Authorization=synthetic.access.jwt; CF_Binding=bound",new Vault(context).profile(id).getString("accessCookies"));
            assertFalse(vault.profiles().toString().contains("synthetic.access.jwt"));assertFalse(context.getSharedPreferences("remote-vault",0).getString("profiles","").contains("synthetic.access.jwt"));
            assertEquals("",vault.profile(other).optString("accessCookies"));
            java.net.HttpURLConnection authorized=new ServerConnection(vault).open(id,"/v1/status","GET",true);
            assertFalse(authorized.getInstanceFollowRedirects());assertEquals("CF_Authorization=synthetic.access.jwt; CF_Binding=bound",authorized.getRequestProperty("Cookie"));assertEquals("fixture-agent",authorized.getRequestProperty("User-Agent"));authorized.disconnect();
            java.net.HttpURLConnection isolated=new ServerConnection(vault).open(other,"/v1/status","GET",true);assertNull(isolated.getRequestProperty("Cookie"));isolated.disconnect();
            long previous=vault.profile(id).optLong("accessVersion");vault.clearAccess(id);
            try{vault.finishAccess(id,"https://access.example",previous,"CF_Authorization=stale","fixture-agent");fail("A delayed sign-in restored forgotten Access credentials");}catch(IllegalStateException expected){}
            assertEquals("",vault.profile(id).optString("accessCookies"));
            vault.finishAccess(id,"https://access.example",vault.profile(id).optLong("accessVersion"),"CF_Authorization=fresh","fixture-agent");
            vault.update(id,"Replacement","https://changed.example");assertEquals("",vault.profile(id).optString("accessCookies"));
        }finally{vault.remove(id);vault.remove(other);}
    }
    @Test public void credentialsSurviveRecreationWithoutBeingExposedToTheWebUI() throws Exception {
        Context context=InstrumentationRegistry.getInstrumentation().getTargetContext();
        Vault vault=new Vault(context);String id=vault.add("Instrumented test","https://server.example");
        try{vault.set(id,"token","synthetic-test-token-32-characters-long");Vault reopened=new Vault(context);assertEquals("synthetic-test-token-32-characters-long",reopened.profile(id).getString("token"));assertFalse(reopened.profiles().toString().contains("synthetic-test-token"));assertFalse(context.getSharedPreferences("remote-vault",0).getString("profiles","").contains("synthetic-test-token"));}
        finally{vault.remove(id);}
    }

    @Test public void editingAnOriginClearsTrustAndRejectsRepliesFromThePreviousServer() throws Exception {
        Context context=InstrumentationRegistry.getInstrumentation().getTargetContext();
        Vault vault=new Vault(context);String id=vault.add("Original","https://original.example");
        try{
            vault.setCredential(id,"https://original.example","synthetic-test-token-32-characters-long");
            vault.set(id,"certificates","synthetic certificate fixture");vault.set(id,"route","#page=workers&worker=one");
            assertFalse(vault.update(id,"Renamed","https://original.example/"));
            assertEquals("synthetic-test-token-32-characters-long",vault.profile(id).getString("token"));
            assertTrue(vault.update(id,"Replacement","https://replacement.example"));
            assertEquals("",vault.profile(id).getString("token"));assertEquals("",vault.profile(id).getString("certificates"));assertEquals("",vault.profile(id).getString("route"));
            try{vault.setCredential(id,"https://original.example","old-server-reply");fail("A stale pairing reply must not cross server origins");}catch(IllegalStateException expected){}
            try{new ServerConnection(vault).open(id,"/v1/status","GET",true,"https://original.example");fail("An old activity must not connect to a replacement server");}catch(IllegalStateException expected){}
            vault.set(id,"route","#page=settings");
            assertEquals("#page=settings",new Vault(context).profile(id).getString("route"));
        }finally{vault.remove(id);}
    }

    @Test public void forgettingPairingRejectsAReplyAlreadyInFlightToTheSameOrigin()throws Exception{
        Context context=InstrumentationRegistry.getInstrumentation().getTargetContext();
        Vault vault=new Vault(context);String url="https://pairing.example",id=vault.add("Pairing fixture",url);
        try{
            long started=vault.profile(id).optLong("credentialVersion");
            vault.setCredential(id,url,"");
            try{vault.finishPairing(id,url,started,"stale-token");fail("An old pairing reply must not restore a forgotten credential");}catch(IllegalStateException expected){}
            assertEquals("",vault.profile(id).getString("token"));
            long fresh=vault.profile(id).optLong("credentialVersion");
            vault.finishPairing(id,url,fresh,"fresh-synthetic-device-credential");
            assertEquals("fresh-synthetic-device-credential",new Vault(context).profile(id).getString("token"));
        }finally{vault.remove(id);}
    }
}
