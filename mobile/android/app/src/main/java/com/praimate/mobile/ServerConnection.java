package com.praimate.mobile;

import org.json.JSONObject;
import java.io.ByteArrayInputStream;
import java.net.HttpURLConnection;
import java.net.URL;
import java.nio.charset.StandardCharsets;
import java.security.KeyStore;
import java.security.SecureRandom;
import java.security.cert.CertificateFactory;
import java.security.cert.X509Certificate;
import java.util.Collection;
import javax.net.ssl.HttpsURLConnection;
import javax.net.ssl.SSLContext;
import javax.net.ssl.TrustManager;
import javax.net.ssl.TrustManagerFactory;
import javax.net.ssl.X509TrustManager;

public final class ServerConnection {
    private static final java.util.concurrent.ConcurrentHashMap<String,Factory> factories=new java.util.concurrent.ConcurrentHashMap<>();
    private static final class Factory { final String pem;final javax.net.ssl.SSLSocketFactory socket;Factory(String pem,javax.net.ssl.SSLSocketFactory socket){this.pem=pem;this.socket=socket;} }
    private final Vault vault;
    public ServerConnection(Vault vault) { this.vault=vault; }

    public HttpURLConnection open(String profileID, String path, String method, boolean authenticate) throws Exception {
        return open(profileID,path,method,authenticate,null);
    }
    public HttpURLConnection open(String profileID, String path, String method, boolean authenticate,String expectedURL) throws Exception {
        JSONObject profile=vault.profile(profileID);
        if(expectedURL!=null&&!profile.getString("url").equals(expectedURL))throw new IllegalStateException("Server address changed. Reopen this server before continuing.");
        HttpURLConnection connection=(HttpURLConnection)new URL(profile.getString("url")+Endpoint.apiPath(path)).openConnection();
        connection.setInstanceFollowRedirects(false); connection.setConnectTimeout(15000); connection.setReadTimeout(45000); connection.setRequestMethod(method);
        connection.setRequestProperty("Accept-Encoding","identity");
        String access=AccessSession.cookies(profile.optString("accessCookies"));
        if(!access.isEmpty())connection.setRequestProperty("Cookie",access);
        if(!profile.optString("accessUserAgent").isEmpty())connection.setRequestProperty("User-Agent",profile.getString("accessUserAgent"));
        if(authenticate&&!profile.optString("token").isEmpty())connection.setRequestProperty("Authorization","Bearer "+profile.getString("token"));
        if(connection instanceof HttpsURLConnection && !profile.optString("certificates").isEmpty()) {
            String pem=profile.getString("certificates");Factory cached=factories.get(profileID);
            if(cached==null||!cached.pem.equals(pem)){
                SSLContext tls=SSLContext.getInstance("TLS");tls.init(null,new TrustManager[]{trust(pem)},new SecureRandom());
                cached=new Factory(pem,tls.getSocketFactory());factories.put(profileID,cached);
            }
            ((HttpsURLConnection)connection).setSSLSocketFactory(cached.socket);
            // Keep HttpsURLConnection's hostname verification, including for
            // explicitly trusted private CAs and self-signed certificates.
        }
        return connection;
    }

    /** Redirects remain disabled: PrAImate bearers must never reach login/IdP hosts. */
    public static int responseCode(HttpURLConnection connection)throws Exception{
        int code=connection.getResponseCode();
        if(AccessSession.challenge(code,connection.getHeaderField("Location"),connection.getContentType(),connection.getHeaderField("CF-Ray")!=null||"challenge".equals(connection.getHeaderField("cf-mitigated"))))throw new AccessSession.Required();
        return code;
    }

    private static X509TrustManager manager(KeyStore store) throws Exception {
        TrustManagerFactory factory=TrustManagerFactory.getInstance(TrustManagerFactory.getDefaultAlgorithm()); factory.init(store);
        for(TrustManager item:factory.getTrustManagers())if(item instanceof X509TrustManager)return (X509TrustManager)item;
        throw new IllegalStateException("Certificate validation is unavailable");
    }
    private static X509TrustManager trust(String pem) throws Exception {
        X509TrustManager system=manager(null);
        KeyStore store=KeyStore.getInstance(KeyStore.getDefaultType());store.load(null);
        Collection<? extends java.security.cert.Certificate> certificates=CertificateFactory.getInstance("X.509").generateCertificates(new ByteArrayInputStream(pem.getBytes(StandardCharsets.UTF_8)));
        if(certificates.isEmpty())throw new IllegalArgumentException("The selected file contains no CA certificates");
        int i=0;for(java.security.cert.Certificate certificate:certificates){((X509Certificate)certificate).checkValidity();store.setCertificateEntry("server-"+(i++),certificate);}
        X509TrustManager custom=manager(store);
        return new X509TrustManager(){
            public X509Certificate[] getAcceptedIssuers(){return custom.getAcceptedIssuers();}
            public void checkClientTrusted(X509Certificate[] chain,String auth)throws java.security.cert.CertificateException{system.checkClientTrusted(chain,auth);}
            public void checkServerTrusted(X509Certificate[] chain,String auth)throws java.security.cert.CertificateException{try{system.checkServerTrusted(chain,auth);}catch(java.security.cert.CertificateException error){custom.checkServerTrusted(chain,auth);}}
        };
    }
}
