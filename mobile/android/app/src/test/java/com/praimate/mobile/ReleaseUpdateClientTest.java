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
}
