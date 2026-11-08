package com.praimate.mobile;
import org.junit.Test;
import static org.junit.Assert.*;
public class ReleaseUpdateClientTest {
    private static final String ASSET="{\"name\":\"praimate-mobile-preview.apk\",\"size\":5000000,\"digest\":\"sha256:"+"a".repeat(64)+"\"}";
    @Test public void selectsNewestCompatibleApkInsteadOfNewestDesktopRelease()throws Exception{
        String json="[{\"tag_name\":\"v99.0.0\",\"assets\":[]},{\"tag_name\":\"v1.2.28\",\"draft\":true,\"assets\":["+ASSET+"]},{\"tag_name\":\"v1.2.27\",\"assets\":["+ASSET+"]}]";
        ReleaseUpdateClient.Info found=new ReleaseUpdateClient("1.2.26-server-debug",true).select(json);
        assertNotNull(found);assertEquals("v1.2.27",found.tag);assertEquals("a".repeat(64),found.sha256);assertEquals(UpdatePolicy.releaseURL(found.tag,found.asset),found.url);
        assertNull(new ReleaseUpdateClient("1.2.27-debug",true).select(json));
        assertNull(new ReleaseUpdateClient("1.2.26",false).select(json));
    }
    @Test public void rejectsOversizedApksAndUnrelatedAssets()throws Exception{
        assertNull(new ReleaseUpdateClient("1.2.26-debug",true).select("[{\"tag_name\":\"v1.2.27\",\"assets\":[{\"name\":\"praimate-mobile-preview.apk\",\"size\":999999999}]}]"));
        assertNull(new ReleaseUpdateClient("1.2.26-debug",true).select("[{\"tag_name\":\"v1.2.27\",\"assets\":[{\"name\":\"other.apk\",\"size\":500}]}]"));
    }
    @Test public void androidCanUpdateWhileTheHostReleaseVersionStaysTheSame()throws Exception{
        String json="[{\"tag_name\":\"v1.3.3\",\"body\":\"<!-- praimate-android-version:1.3.5 -->\",\"assets\":["+ASSET+"]},{\"tag_name\":\"v1.3.4\",\"assets\":["+ASSET+"]}]";
        ReleaseUpdateClient.Info found=new ReleaseUpdateClient("1.3.4-debug",true).select(json);
        assertNotNull(found);assertEquals("1.3.5",found.version);assertEquals("v1.3.3",found.tag);
        assertTrue(found.url.contains("/download/v1.3.3/"));
        assertEquals("1.3.5",ReleaseUpdateClient.Info.fromJSON(found.json()).version);
        assertNull(new ReleaseUpdateClient("1.3.5-debug",true).select(json));
    }
    @Test public void oldCachedCandidatesAndDuplicateVersionMarkersAreHandled()throws Exception{
        org.json.JSONObject old=new org.json.JSONObject().put("tag","v1.3.3").put("asset","praimate-mobile-preview.apk").put("sha256","a".repeat(64)).put("size",500);
        assertEquals("v1.3.3",ReleaseUpdateClient.Info.fromJSON(old).version);
        assertNull(new ReleaseUpdateClient("1.3.3-debug",true).select("[{\"tag_name\":\"v1.3.3\",\"body\":\"<!-- praimate-android-version:1.3.4 --> <!-- praimate-android-version:1.3.5 -->\",\"assets\":["+ASSET+"]}]"));
    }
}
